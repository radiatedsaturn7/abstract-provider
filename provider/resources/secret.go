package resources

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"abstract-provider/provider/shared"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/keyvault/azsecrets"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	schema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	secretmanager "google.golang.org/api/secretmanager/v1"
)

type SecretResource struct {
	sm        *secretsmanager.Client
	azureCred azcore.TokenCredential
	gcp       *secretmanager.Service
	gcpProj   string
}

func NewSecretResource() resource.Resource { return &SecretResource{} }

func (r *SecretResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	cfg, ok := req.ProviderData.(*shared.ProviderConfig)
	if !ok {
		resp.Diagnostics.AddError("invalid provider data", "")
		return
	}
	r.sm = cfg.AWSSM
	r.azureCred = cfg.AzureCred
	r.gcp = cfg.GCPSecrets
	r.gcpProj = cfg.GCPProject
}

func (r *SecretResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "abstract_secret"
}

func (r *SecretResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id":    schema.StringAttribute{Computed: true},
			"name":  schema.StringAttribute{Required: true},
			"type":  schema.StringAttribute{Required: true},
			"value": schema.StringAttribute{Required: true, Sensitive: true},
		},
	}
}

func (r *SecretResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan struct {
		Name  types.String `tfsdk:"name"`
		Type  types.String `tfsdk:"type"`
		Value types.String `tfsdk:"value"`
	}
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	switch plan.Type.ValueString() {
	case "aws":
		if r.sm == nil {
			resp.Diagnostics.AddError("aws", "missing client")
			return
		}
		out, err := r.sm.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
			Name:         aws.String(plan.Name.ValueString()),
			SecretString: aws.String(plan.Value.ValueString()),
		})
		if err != nil {
			resp.Diagnostics.AddError("aws create", err.Error())
			return
		}
		resp.State.Set(ctx, map[string]interface{}{
			"id":   aws.ToString(out.ARN),
			"name": plan.Name.ValueString(),
			"type": plan.Type.ValueString(),
		})
	case "azure":
		if r.azureCred == nil {
			resp.Diagnostics.AddError("azure", "missing credential")
			return
		}
		vaultURL := os.Getenv("AZURE_KEY_VAULT_URL")
		if vaultURL == "" {
			resp.Diagnostics.AddError("azure", "AZURE_KEY_VAULT_URL not set")
			return
		}
		client, err := azsecrets.NewClient(vaultURL, r.azureCred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure client", err.Error())
			return
		}
		_, err = client.SetSecret(ctx, plan.Name.ValueString(), plan.Value.ValueString(), nil)
		if err != nil {
			resp.Diagnostics.AddError("azure set", err.Error())
			return
		}
		resp.State.Set(ctx, map[string]interface{}{
			"id":   fmt.Sprintf("%s#%s", vaultURL, plan.Name.ValueString()),
			"name": plan.Name.ValueString(),
			"type": plan.Type.ValueString(),
		})
	case "gcp":
		if r.gcp == nil {
			resp.Diagnostics.AddError("gcp", "missing client")
			return
		}
		parent := fmt.Sprintf("projects/%s", r.gcpProj)
		sec := &secretmanager.Secret{Replication: &secretmanager.Replication{Automatic: &secretmanager.Automatic{}}}
		_, err := r.gcp.Projects.Secrets.Create(parent, sec).SecretId(plan.Name.ValueString()).Context(ctx).Do()
		if err != nil && !strings.Contains(err.Error(), "Already exists") {
			resp.Diagnostics.AddError("gcp create", err.Error())
			return
		}
		payload := &secretmanager.SecretPayload{Data: base64.StdEncoding.EncodeToString([]byte(plan.Value.ValueString()))}
		_, err = r.gcp.Projects.Secrets.AddVersion(fmt.Sprintf("projects/%s/secrets/%s", r.gcpProj, plan.Name.ValueString()), &secretmanager.AddSecretVersionRequest{Payload: payload}).Context(ctx).Do()
		if err != nil {
			resp.Diagnostics.AddError("gcp version", err.Error())
			return
		}
		resp.State.Set(ctx, map[string]interface{}{
			"id":   fmt.Sprintf("%s/secrets/%s", parent, plan.Name.ValueString()),
			"name": plan.Name.ValueString(),
			"type": plan.Type.ValueString(),
		})
	default:
		resp.Diagnostics.AddError("unsupported cloud", "")
	}
}

func (r *SecretResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state struct {
		ID   types.String `tfsdk:"id"`
		Name types.String `tfsdk:"name"`
		Type types.String `tfsdk:"type"`
	}
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	switch state.Type.ValueString() {
	case "aws":
		if r.sm == nil {
			return
		}
		_, err := r.sm.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(state.Name.ValueString())})
		if err != nil {
			resp.State.RemoveResource(ctx)
		}
	case "azure":
		if r.azureCred == nil {
			return
		}
		vaultURL := os.Getenv("AZURE_KEY_VAULT_URL")
		if vaultURL == "" {
			resp.State.RemoveResource(ctx)
			return
		}
		client, err := azsecrets.NewClient(vaultURL, r.azureCred, nil)
		if err != nil {
			resp.State.RemoveResource(ctx)
			return
		}
		_, err = client.GetSecret(ctx, state.Name.ValueString(), nil)
		if err != nil {
			resp.State.RemoveResource(ctx)
		}
	case "gcp":
		if r.gcp == nil {
			return
		}
		_, err := r.gcp.Projects.Secrets.Get(fmt.Sprintf("projects/%s/secrets/%s", r.gcpProj, state.Name.ValueString())).Context(ctx).Do()
		if err != nil {
			resp.State.RemoveResource(ctx)
		}
	}
}

func (r *SecretResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan struct {
		Name  types.String `tfsdk:"name"`
		Type  types.String `tfsdk:"type"`
		Value types.String `tfsdk:"value"`
	}
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	delReq := resource.DeleteRequest{State: req.State}
	delResp := &resource.DeleteResponse{}
	r.Delete(ctx, delReq, delResp)
	if delResp.Diagnostics.HasError() {
		resp.Diagnostics.Append(delResp.Diagnostics...)
		return
	}
	createReq := resource.CreateRequest{Plan: req.Plan}
	createResp := &resource.CreateResponse{}
	r.Create(ctx, createReq, createResp)
	resp.Diagnostics.Append(createResp.Diagnostics...)
}

func (r *SecretResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state struct {
		Name types.String `tfsdk:"name"`
		Type types.String `tfsdk:"type"`
	}
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	switch state.Type.ValueString() {
	case "aws":
		if r.sm == nil {
			return
		}
		_, err := r.sm.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{SecretId: aws.String(state.Name.ValueString()), ForceDeleteWithoutRecovery: aws.Bool(true)})
		if err != nil {
			resp.Diagnostics.AddError("aws delete", err.Error())
		}
	case "azure":
		if r.azureCred == nil {
			return
		}
		vaultURL := os.Getenv("AZURE_KEY_VAULT_URL")
		if vaultURL == "" {
			return
		}
		client, err := azsecrets.NewClient(vaultURL, r.azureCred, nil)
		if err != nil {
			return
		}
		_, err = client.BeginDeleteSecret(ctx, state.Name.ValueString(), nil)
		if err != nil {
			resp.Diagnostics.AddError("azure delete", err.Error())
		}
	case "gcp":
		if r.gcp == nil {
			return
		}
		_, err := r.gcp.Projects.Secrets.Delete(fmt.Sprintf("projects/%s/secrets/%s", r.gcpProj, state.Name.ValueString())).Context(ctx).Do()
		if err != nil {
			resp.Diagnostics.AddError("gcp delete", err.Error())
		}
	}
}
