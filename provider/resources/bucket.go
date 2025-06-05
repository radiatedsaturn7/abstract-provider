package resources

import (
	"context"
	"strings"

	"abstract-provider/provider/shared"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"cloud.google.com/go/storage"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	schema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type BucketResource struct {
	s3         *s3.Client
	azureRG    *armresources.ResourceGroupsClient
	azureAcct  *armstorage.AccountsClient
	azureCont  *armstorage.BlobContainersClient
	azureCred  azcore.TokenCredential
	azureSubID string
	azureLoc   string
	gcpStorage *storage.Client
	gcpProject string
	gcpRegion  string
}

func NewBucketResource() resource.Resource {
	return &BucketResource{}
}

func (r *BucketResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	cfg, ok := req.ProviderData.(*shared.ProviderConfig)
	if !ok {
		resp.Diagnostics.AddError("invalid provider data", "")
		return
	}
	r.s3 = cfg.AWSS3
	r.azureRG = cfg.AzureRGClient
	r.azureAcct = cfg.AzureStorageAcct
	r.azureCont = cfg.AzureBlobContainers
	r.azureCred = cfg.AzureCred
	r.azureSubID = cfg.AzureSubID
	r.azureLoc = cfg.AzureLocation
	r.gcpStorage = cfg.GCPStorage
	r.gcpProject = cfg.GCPProject
	r.gcpRegion = cfg.GCPRegion
}

func (r *BucketResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "abstract_bucket"
}

func (r *BucketResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{Computed: true},
			"name":       schema.StringAttribute{Required: true},
			"type":       schema.StringAttribute{Required: true},
			"region":     schema.StringAttribute{Optional: true},
			"versioning": schema.BoolAttribute{Optional: true},
		},
	}
}

func (r *BucketResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan struct {
		Name       types.String `tfsdk:"name"`
		Type       types.String `tfsdk:"type"`
		Region     types.String `tfsdk:"region"`
		Versioning types.Bool   `tfsdk:"versioning"`
	}

	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	switch plan.Type.ValueString() {
	case "aws":
		input := &s3.CreateBucketInput{Bucket: aws.String(plan.Name.ValueString())}
		if plan.Region.ValueString() != "" {
			input.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{LocationConstraint: s3types.BucketLocationConstraint(plan.Region.ValueString())}
		}
		_, err := r.s3.CreateBucket(ctx, input)
		if err != nil {
			resp.Diagnostics.AddError("aws create", err.Error())
			return
		}
		if plan.Versioning.ValueBool() {
			_, err = r.s3.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
				Bucket:                  aws.String(plan.Name.ValueString()),
				VersioningConfiguration: &s3types.VersioningConfiguration{Status: s3types.BucketVersioningStatusEnabled},
			})
			if err != nil {
				resp.Diagnostics.AddError("aws versioning", err.Error())
				return
			}
		}
		resp.State.Set(ctx, map[string]interface{}{
			"id":         plan.Name.ValueString(),
			"name":       plan.Name.ValueString(),
			"type":       plan.Type.ValueString(),
			"region":     plan.Region.ValueString(),
			"versioning": plan.Versioning.ValueBool(),
		})
	case "azure":
		if r.azureAcct == nil || r.azureCont == nil || r.azureRG == nil {
			resp.Diagnostics.AddError("azure", "missing client")
			return
		}
		rgName := "abstract-rg"
		if r.azureLoc == "" && plan.Region.ValueString() != "" {
			r.azureLoc = plan.Region.ValueString()
		}
		_, err := r.azureRG.CreateOrUpdate(ctx, rgName, armresources.ResourceGroup{Location: &r.azureLoc}, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure rg", err.Error())
			return
		}
		acctName := plan.Name.ValueString()
		acctName = strings.ToLower(acctName)
		if len(acctName) > 24 {
			acctName = acctName[:24]
		}
		poller, err := r.azureAcct.BeginCreate(ctx, rgName, acctName, armstorage.AccountCreateParameters{
			Location: &r.azureLoc,
			Kind:     to.Ptr(armstorage.KindStorageV2),
			SKU:      &armstorage.SKU{Name: to.Ptr(armstorage.SKUNameStandardLRS)},
		}, nil)
		if err == nil {
			_, err = poller.PollUntilDone(ctx, nil)
		}
		if err != nil {
			resp.Diagnostics.AddError("azure create account", err.Error())
			return
		}
		keys, err := r.azureAcct.ListKeys(ctx, rgName, acctName, nil)
		if err != nil || keys.Keys == nil || len(keys.Keys) == 0 {
			resp.Diagnostics.AddError("azure keys", "unable to get account key")
			return
		}
		key := *keys.Keys[0].Value
		cred, err := azblob.NewSharedKeyCredential(acctName, key)
		if err != nil {
			resp.Diagnostics.AddError("azure cred", err.Error())
			return
		}
		svc, err := azblob.NewClientWithSharedKeyCredential("https://"+acctName+".blob.core.windows.net/", cred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure svc", err.Error())
			return
		}
		_, err = svc.CreateContainer(ctx, plan.Name.ValueString(), nil)
		if err != nil {
			resp.Diagnostics.AddError("azure container", err.Error())
			return
		}
		resp.State.Set(ctx, map[string]interface{}{
			"id":             plan.Name.ValueString(),
			"name":           plan.Name.ValueString(),
			"type":           plan.Type.ValueString(),
			"region":         r.azureLoc,
			"versioning":     plan.Versioning.ValueBool(),
			"account":        acctName,
			"resource_group": rgName,
		})
	case "gcp":
		if r.gcpStorage == nil {
			resp.Diagnostics.AddError("gcp", "missing client")
			return
		}
		region := plan.Region.ValueString()
		if region == "" {
			region = r.gcpRegion
		}
		attrs := &storage.BucketAttrs{Location: region}
		if plan.Versioning.ValueBool() {
			attrs.VersioningEnabled = true
		}
		err := r.gcpStorage.Bucket(plan.Name.ValueString()).Create(ctx, r.gcpProject, attrs)
		if err != nil {
			resp.Diagnostics.AddError("gcp create", err.Error())
			return
		}
		resp.State.Set(ctx, map[string]interface{}{
			"id":         plan.Name.ValueString(),
			"name":       plan.Name.ValueString(),
			"type":       plan.Type.ValueString(),
			"region":     region,
			"versioning": plan.Versioning.ValueBool(),
			"project":    r.gcpProject,
		})
	default:
		resp.Diagnostics.AddError("unsupported cloud", "only aws implemented")
	}
}

func (r *BucketResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state struct {
		ID            types.String `tfsdk:"id"`
		Type          types.String `tfsdk:"type"`
		Account       types.String `tfsdk:"account"`
		ResourceGroup types.String `tfsdk:"resource_group"`
		Project       types.String `tfsdk:"project"`
	}
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	switch state.Type.ValueString() {
	case "aws":
		_, err := r.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(state.ID.ValueString())})
		if err != nil {
			resp.Diagnostics.AddError("aws read", err.Error())
			resp.State.RemoveResource(ctx)
		}
	case "azure":
		if r.azureAcct == nil || r.azureCont == nil {
			resp.Diagnostics.AddError("azure", "missing client")
			return
		}
		keys, err := r.azureAcct.ListKeys(ctx, state.ResourceGroup.ValueString(), state.Account.ValueString(), nil)
		if err != nil || keys.Keys == nil || len(keys.Keys) == 0 {
			resp.Diagnostics.AddError("azure keys", "unable to get account key")
			resp.State.RemoveResource(ctx)
			return
		}
		key := *keys.Keys[0].Value
		cred, err := azblob.NewSharedKeyCredential(state.Account.ValueString(), key)
		if err != nil {
			resp.Diagnostics.AddError("azure cred", err.Error())
			return
		}
		svc, err := azblob.NewClientWithSharedKeyCredential("https://"+state.Account.ValueString()+".blob.core.windows.net/", cred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure svc", err.Error())
			return
		}
		cont := svc.ServiceClient().NewContainerClient(state.ID.ValueString())
		_, err = cont.GetProperties(ctx, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure read", err.Error())
			resp.State.RemoveResource(ctx)
		}
	case "gcp":
		if r.gcpStorage == nil {
			resp.Diagnostics.AddError("gcp", "missing client")
			return
		}
		_, err := r.gcpStorage.Bucket(state.ID.ValueString()).Attrs(ctx)
		if err != nil {
			resp.Diagnostics.AddError("gcp read", err.Error())
			resp.State.RemoveResource(ctx)
		}
	}
}

func (r *BucketResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan struct {
		Name       types.String `tfsdk:"name"`
		Type       types.String `tfsdk:"type"`
		Versioning types.Bool   `tfsdk:"versioning"`
	}
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	switch plan.Type.ValueString() {
	case "aws":
		status := s3types.BucketVersioningStatusSuspended
		if plan.Versioning.ValueBool() {
			status = s3types.BucketVersioningStatusEnabled
		}
		_, err := r.s3.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
			Bucket:                  aws.String(plan.Name.ValueString()),
			VersioningConfiguration: &s3types.VersioningConfiguration{Status: status},
		})
		if err != nil {
			resp.Diagnostics.AddError("aws update", err.Error())
			return
		}
	case "gcp":
		if r.gcpStorage == nil {
			resp.Diagnostics.AddError("gcp", "missing client")
			return
		}
		_, err := r.gcpStorage.Bucket(plan.Name.ValueString()).Update(ctx, storage.BucketAttrsToUpdate{
			VersioningEnabled: plan.Versioning.ValueBool(),
		})
		if err != nil {
			resp.Diagnostics.AddError("gcp update", err.Error())
			return
		}
	}
}

func (r *BucketResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state struct {
		ID            types.String `tfsdk:"id"`
		Type          types.String `tfsdk:"type"`
		Account       types.String `tfsdk:"account"`
		ResourceGroup types.String `tfsdk:"resource_group"`
		Project       types.String `tfsdk:"project"`
	}
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	switch state.Type.ValueString() {
	case "aws":
		_, err := r.s3.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(state.ID.ValueString())})
		if err != nil {
			resp.Diagnostics.AddError("aws delete", err.Error())
		}
	case "azure":
		if r.azureAcct == nil || r.azureCont == nil {
			resp.Diagnostics.AddError("azure", "missing client")
			return
		}
		keys, err := r.azureAcct.ListKeys(ctx, state.ResourceGroup.ValueString(), state.Account.ValueString(), nil)
		if err != nil || keys.Keys == nil || len(keys.Keys) == 0 {
			resp.Diagnostics.AddError("azure keys", "unable to get account key")
			return
		}
		key := *keys.Keys[0].Value
		cred, err := azblob.NewSharedKeyCredential(state.Account.ValueString(), key)
		if err != nil {
			resp.Diagnostics.AddError("azure cred", err.Error())
			return
		}
		svc, err := azblob.NewClientWithSharedKeyCredential("https://"+state.Account.ValueString()+".blob.core.windows.net/", cred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure svc", err.Error())
			return
		}
		_, err = svc.DeleteContainer(ctx, state.ID.ValueString(), nil)
		if err != nil {
			resp.Diagnostics.AddError("azure delete", err.Error())
		}
		// also delete storage account
		_, err = r.azureAcct.Delete(ctx, state.ResourceGroup.ValueString(), state.Account.ValueString(), nil)
		if err != nil {
			resp.Diagnostics.AddError("azure delete account", err.Error())
		}
	case "gcp":
		if r.gcpStorage == nil {
			resp.Diagnostics.AddError("gcp", "missing client")
			return
		}
		err := r.gcpStorage.Bucket(state.ID.ValueString()).Delete(ctx)
		if err != nil {
			resp.Diagnostics.AddError("gcp delete", err.Error())
		}
	}
}
