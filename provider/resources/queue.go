package resources

import (
	"context"
	"fmt"
	"strings"

	"abstract-provider/provider/shared"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azqueue"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	schema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type QueueResource struct {
	sqs        *sqs.Client
	azureRG    *armresources.ResourceGroupsClient
	azureAcct  *armstorage.AccountsClient
	azureCred  azcore.TokenCredential
	azureSubID string
	azureLoc   string
}

func NewQueueResource() resource.Resource { return &QueueResource{} }

func (r *QueueResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	cfg, ok := req.ProviderData.(*shared.ProviderConfig)
	if !ok {
		resp.Diagnostics.AddError("invalid provider data", "")
		return
	}
	r.sqs = cfg.AWSSQS
	r.azureRG = cfg.AzureRGClient
	r.azureAcct = cfg.AzureStorageAcct
	r.azureCred = cfg.AzureCred
	r.azureSubID = cfg.AzureSubID
	r.azureLoc = cfg.AzureLocation
}

func (r *QueueResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "abstract_queue"
}

func (r *QueueResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id":             schema.StringAttribute{Computed: true},
			"name":           schema.StringAttribute{Required: true},
			"type":           schema.StringAttribute{Required: true},
			"region":         schema.StringAttribute{Optional: true},
			"fifo":           schema.BoolAttribute{Optional: true},
			"account":        schema.StringAttribute{Computed: true},
			"resource_group": schema.StringAttribute{Computed: true},
		},
	}
}

func (r *QueueResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan struct {
		Name   types.String `tfsdk:"name"`
		Type   types.String `tfsdk:"type"`
		Region types.String `tfsdk:"region"`
		FIFO   types.Bool   `tfsdk:"fifo"`
	}
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	switch plan.Type.ValueString() {
	case "aws":
		if r.sqs == nil {
			resp.Diagnostics.AddError("aws", "missing client")
			return
		}
		name := plan.Name.ValueString()
		input := &sqs.CreateQueueInput{QueueName: aws.String(name)}
		if plan.FIFO.ValueBool() {
			if !strings.HasSuffix(name, ".fifo") {
				name += ".fifo"
			}
			input.QueueName = aws.String(name)
			input.Attributes = map[string]string{"FifoQueue": "true"}
		}
		out, err := r.sqs.CreateQueue(ctx, input)
		if err != nil {
			resp.Diagnostics.AddError("aws create", err.Error())
			return
		}
		resp.State.Set(ctx, map[string]interface{}{
			"id":   aws.ToString(out.QueueUrl),
			"name": name,
			"type": plan.Type.ValueString(),
			"fifo": plan.FIFO.ValueBool(),
		})
	case "azure":
		if r.azureAcct == nil || r.azureRG == nil {
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
		acctName := strings.ToLower(plan.Name.ValueString())
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
		cred, err := azqueue.NewSharedKeyCredential(acctName, key)
		if err != nil {
			resp.Diagnostics.AddError("azure cred", err.Error())
			return
		}
		svc, err := azqueue.NewServiceClientWithSharedKey(fmt.Sprintf("https://%s.queue.core.windows.net/", acctName), cred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure service", err.Error())
			return
		}
		_, err = svc.CreateQueue(ctx, plan.Name.ValueString(), nil)
		if err != nil {
			resp.Diagnostics.AddError("azure create queue", err.Error())
			return
		}
		resp.State.Set(ctx, map[string]interface{}{
			"id":             plan.Name.ValueString(),
			"name":           plan.Name.ValueString(),
			"type":           plan.Type.ValueString(),
			"region":         r.azureLoc,
			"fifo":           plan.FIFO.ValueBool(),
			"account":        acctName,
			"resource_group": rgName,
		})
	case "gcp":
		resp.Diagnostics.AddError("gcp", "queue resource not implemented")
	default:
		resp.Diagnostics.AddError("unsupported cloud", "only aws and azure implemented")
	}
}

func (r *QueueResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state struct {
		ID            types.String `tfsdk:"id"`
		Type          types.String `tfsdk:"type"`
		Account       types.String `tfsdk:"account"`
		ResourceGroup types.String `tfsdk:"resource_group"`
	}
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	switch state.Type.ValueString() {
	case "aws":
		if r.sqs == nil {
			return
		}
		_, err := r.sqs.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: aws.String(state.ID.ValueString()), AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn}})
		if err != nil {
			resp.State.RemoveResource(ctx)
		}
	case "azure":
		if r.azureAcct == nil {
			return
		}
		keys, err := r.azureAcct.ListKeys(ctx, state.ResourceGroup.ValueString(), state.Account.ValueString(), nil)
		if err != nil || keys.Keys == nil || len(keys.Keys) == 0 {
			resp.State.RemoveResource(ctx)
			return
		}
		key := *keys.Keys[0].Value
		cred, err := azqueue.NewSharedKeyCredential(state.Account.ValueString(), key)
		if err != nil {
			resp.State.RemoveResource(ctx)
			return
		}
		svc, err := azqueue.NewServiceClientWithSharedKey(fmt.Sprintf("https://%s.queue.core.windows.net/", state.Account.ValueString()), cred, nil)
		if err != nil {
			resp.State.RemoveResource(ctx)
			return
		}
		_, err = svc.NewQueueClient(state.ID.ValueString()).GetProperties(ctx, nil)
		if err != nil {
			resp.State.RemoveResource(ctx)
		}
	}
}

func (r *QueueResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// no updatable fields for now
}

func (r *QueueResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state struct {
		ID            types.String `tfsdk:"id"`
		Type          types.String `tfsdk:"type"`
		Account       types.String `tfsdk:"account"`
		ResourceGroup types.String `tfsdk:"resource_group"`
	}
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	switch state.Type.ValueString() {
	case "aws":
		if r.sqs == nil {
			return
		}
		_, err := r.sqs.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(state.ID.ValueString())})
		if err != nil {
			resp.Diagnostics.AddError("aws delete", err.Error())
		}
	case "azure":
		if r.azureAcct == nil {
			return
		}
		keys, err := r.azureAcct.ListKeys(ctx, state.ResourceGroup.ValueString(), state.Account.ValueString(), nil)
		if err != nil || keys.Keys == nil || len(keys.Keys) == 0 {
			resp.Diagnostics.AddError("azure keys", "unable to get account key")
			return
		}
		key := *keys.Keys[0].Value
		cred, err := azqueue.NewSharedKeyCredential(state.Account.ValueString(), key)
		if err != nil {
			resp.Diagnostics.AddError("azure cred", err.Error())
			return
		}
		svc, err := azqueue.NewServiceClientWithSharedKey(fmt.Sprintf("https://%s.queue.core.windows.net/", state.Account.ValueString()), cred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure service", err.Error())
			return
		}
		_, err = svc.DeleteQueue(ctx, state.ID.ValueString(), nil)
		if err != nil {
			resp.Diagnostics.AddError("azure delete", err.Error())
		}
		_, err = r.azureAcct.Delete(ctx, state.ResourceGroup.ValueString(), state.Account.ValueString(), nil)
		if err != nil {
			resp.Diagnostics.AddError("azure delete account", err.Error())
		}
	}
}
