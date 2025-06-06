package resources

import (
	"context"
	"fmt"

	"abstract-provider/provider/shared"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	schema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// RegistryResource implements an abstract container registry.
type RegistryResource struct {
	ecr       *ecr.Client
	azureRG   *armresources.ResourceGroupsClient
	azureReg  *armcontainerregistry.RegistriesClient
	azureCred azcore.TokenCredential
	azureSub  string
	azureLoc  string
}

// NewRegistryResource returns a new registry resource.
func NewRegistryResource() resource.Resource { return &RegistryResource{} }

// Configure stores provider configuration data for the resource.
func (r *RegistryResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	cfg, ok := req.ProviderData.(*shared.ProviderConfig)
	if !ok {
		resp.Diagnostics.AddError("invalid provider data", "")
		return
	}
	r.ecr = cfg.AWSECR
	r.azureRG = cfg.AzureRGClient
	r.azureReg = cfg.AzureRegistryClient
	r.azureCred = cfg.AzureCred
	r.azureSub = cfg.AzureSubID
	r.azureLoc = cfg.AzureLocation
}

// Metadata sets the resource type name.
func (r *RegistryResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "abstract_registry"
}

// Schema defines the schema for the registry resource.
func (r *RegistryResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id":             schema.StringAttribute{Computed: true},
			"name":           schema.StringAttribute{Required: true},
			"type":           schema.StringAttribute{Required: true},
			"region":         schema.StringAttribute{Optional: true},
			"login_server":   schema.StringAttribute{Computed: true},
			"resource_group": schema.StringAttribute{Computed: true},
		},
	}
}

// Create provisions a container registry.
func (r *RegistryResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan struct {
		Name   types.String `tfsdk:"name"`
		Type   types.String `tfsdk:"type"`
		Region types.String `tfsdk:"region"`
	}
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	switch plan.Type.ValueString() {
	case "aws":
		if r.ecr == nil {
			resp.Diagnostics.AddError("aws", "missing client")
			return
		}
		out, err := r.ecr.CreateRepository(ctx, &ecr.CreateRepositoryInput{RepositoryName: aws.String(plan.Name.ValueString())})
		if err != nil {
			resp.Diagnostics.AddError("aws create", err.Error())
			return
		}
		arn := aws.ToString(out.Repository.RepositoryArn)
		resp.State.Set(ctx, map[string]interface{}{
			"id":   arn,
			"name": plan.Name.ValueString(),
			"type": plan.Type.ValueString(),
		})
	case "azure":
		if r.azureReg == nil || r.azureRG == nil {
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
		poller, err := r.azureReg.BeginCreate(ctx, rgName, plan.Name.ValueString(), armcontainerregistry.Registry{
			Location: &r.azureLoc,
			SKU:      &armcontainerregistry.SKU{Name: to.Ptr(armcontainerregistry.SKUNameBasic)},
		}, nil)
		if err == nil {
			_, err = poller.PollUntilDone(ctx, nil)
		}
		if err != nil {
			resp.Diagnostics.AddError("azure create", err.Error())
			return
		}
		// fetch properties to get login server
		reg, err := r.azureReg.Get(ctx, rgName, plan.Name.ValueString(), nil)
		if err != nil {
			resp.Diagnostics.AddError("azure get", err.Error())
			return
		}
		login := ""
		if reg.Properties != nil && reg.Properties.LoginServer != nil {
			login = *reg.Properties.LoginServer
		}
		resp.State.Set(ctx, map[string]interface{}{
			"id":             *reg.ID,
			"name":           plan.Name.ValueString(),
			"type":           plan.Type.ValueString(),
			"region":         r.azureLoc,
			"login_server":   login,
			"resource_group": rgName,
		})
	case "gcp":
		resp.Diagnostics.AddError("gcp", "registry resource not implemented")
	default:
		resp.Diagnostics.AddError("unsupported cloud", "only aws and azure implemented")
	}
}

// Read verifies the registry still exists.
func (r *RegistryResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state struct {
		ID            types.String `tfsdk:"id"`
		Type          types.String `tfsdk:"type"`
		Name          types.String `tfsdk:"name"`
		ResourceGroup types.String `tfsdk:"resource_group"`
	}
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	switch state.Type.ValueString() {
	case "aws":
		if r.ecr == nil {
			return
		}
		_, err := r.ecr.DescribeRepositories(ctx, &ecr.DescribeRepositoriesInput{RepositoryNames: []string{state.Name.ValueString()}})
		if err != nil {
			resp.State.RemoveResource(ctx)
		}
	case "azure":
		if r.azureReg == nil {
			return
		}
		_, err := r.azureReg.Get(ctx, state.ResourceGroup.ValueString(), state.Name.ValueString(), nil)
		if err != nil {
			resp.State.RemoveResource(ctx)
		}
	}
}

// Update has no updatable fields currently.
func (r *RegistryResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
}

// Delete removes the registry.
func (r *RegistryResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state struct {
		ID            types.String `tfsdk:"id"`
		Type          types.String `tfsdk:"type"`
		Name          types.String `tfsdk:"name"`
		ResourceGroup types.String `tfsdk:"resource_group"`
	}
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	switch state.Type.ValueString() {
	case "aws":
		if r.ecr == nil {
			return
		}
		_, err := r.ecr.DeleteRepository(ctx, &ecr.DeleteRepositoryInput{RepositoryName: aws.String(state.Name.ValueString()), Force: aws.Bool(true)})
		if err != nil {
			resp.Diagnostics.AddError("aws delete", err.Error())
		}
	case "azure":
		if r.azureReg == nil {
			return
		}
		poller, err := r.azureReg.BeginDelete(ctx, state.ResourceGroup.ValueString(), state.Name.ValueString(), nil)
		if err == nil {
			_, err = poller.PollUntilDone(ctx, nil)
		}
		if err != nil {
			resp.Diagnostics.AddError("azure delete", err.Error())
		}
	}
}
