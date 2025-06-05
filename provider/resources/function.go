package resources

import (
        "context"
        "io/ioutil"
        "net/http"
        "os"
        "strings"
        "time"

	"abstract-provider/provider/shared"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
        lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
        cloudfunctions "google.golang.org/api/cloudfunctions/v1"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	schema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type FunctionResource struct {
        lambda    *lambda.Client
        azureWeb  *armappservice.WebAppsClient
        azurePlan *armappservice.PlansClient
        azureRG   *armresources.ResourceGroupsClient
        azureAcct *armstorage.AccountsClient
        azureCred azcore.TokenCredential
        azureSub  string
        azureLoc  string
        gcpFunc   *cloudfunctions.Service
        gcpProj   string
        gcpRegion string
}

func NewFunctionResource() resource.Resource { return &FunctionResource{} }

func (r *FunctionResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	cfg, ok := req.ProviderData.(*shared.ProviderConfig)
	if !ok {
		resp.Diagnostics.AddError("invalid provider data", "")
		return
	}
	r.lambda = cfg.AWSLambda
	r.azureWeb = cfg.AzureWebClient
	r.azurePlan = cfg.AzurePlanClient
	r.azureRG = cfg.AzureRGClient
        r.azureAcct = cfg.AzureStorageAcct
        r.azureCred = cfg.AzureCred
        r.azureSub = cfg.AzureSubID
        r.azureLoc = cfg.AzureLocation
        r.gcpFunc = cfg.GCPFunctions
        r.gcpProj = cfg.GCPProject
        r.gcpRegion = cfg.GCPRegion
}

func (r *FunctionResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "abstract_function"
}

func (r *FunctionResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id":             schema.StringAttribute{Computed: true},
			"name":           schema.StringAttribute{Required: true},
			"type":           schema.StringAttribute{Required: true},
			"region":         schema.StringAttribute{Optional: true},
			"runtime":        schema.StringAttribute{Required: true},
			"handler":        schema.StringAttribute{Required: true},
			"code":           schema.StringAttribute{Required: true},
			"account":        schema.StringAttribute{Computed: true},
			"plan":           schema.StringAttribute{Computed: true},
			"resource_group": schema.StringAttribute{Computed: true},
		},
	}
}

func (r *FunctionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan struct {
		Name    types.String `tfsdk:"name"`
		Type    types.String `tfsdk:"type"`
		Region  types.String `tfsdk:"region"`
		Runtime types.String `tfsdk:"runtime"`
		Handler types.String `tfsdk:"handler"`
		Code    types.String `tfsdk:"code"`
	}
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	switch plan.Type.ValueString() {
	case "aws":
		if r.lambda == nil {
			resp.Diagnostics.AddError("missing AWS client", "")
			return
		}
		role := os.Getenv("LAMBDA_ROLE_ARN")
		if role == "" {
			resp.Diagnostics.AddError("missing role", "LAMBDA_ROLE_ARN must be set")
			return
		}
		codeBytes, err := ioutil.ReadFile(plan.Code.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("read code", err.Error())
			return
		}
		_, err = r.lambda.CreateFunction(ctx, &lambda.CreateFunctionInput{
			FunctionName: aws.String(plan.Name.ValueString()),
			Runtime:      lambdatypes.Runtime(plan.Runtime.ValueString()),
			Handler:      aws.String(plan.Handler.ValueString()),
			Role:         aws.String(role),
			Code:         &lambdatypes.FunctionCode{ZipFile: codeBytes},
		})
		if err != nil {
			resp.Diagnostics.AddError("aws create", err.Error())
			return
		}
		resp.State.Set(ctx, map[string]interface{}{
			"id":      plan.Name.ValueString(),
			"name":    plan.Name.ValueString(),
			"type":    plan.Type.ValueString(),
			"region":  plan.Region.ValueString(),
			"runtime": plan.Runtime.ValueString(),
			"handler": plan.Handler.ValueString(),
			"code":    plan.Code.ValueString(),
		})
       case "azure":
               if r.azureWeb == nil || r.azurePlan == nil || r.azureRG == nil || r.azureAcct == nil {
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
		acctPoller, err := r.azureAcct.BeginCreate(ctx, rgName, acctName, armstorage.AccountCreateParameters{
			Location: &r.azureLoc,
			Kind:     to.Ptr(armstorage.KindStorageV2),
			SKU:      &armstorage.SKU{Name: to.Ptr(armstorage.SKUNameStandardLRS)},
		}, nil)
		if err == nil {
			_, err = acctPoller.PollUntilDone(ctx, nil)
		}
		if err != nil {
			resp.Diagnostics.AddError("azure storage", err.Error())
			return
		}
		planName := plan.Name.ValueString() + "-plan"
		planPoller, err := r.azurePlan.BeginCreateOrUpdate(ctx, rgName, planName, armappservice.Plan{
			Location: &r.azureLoc,
			Kind:     to.Ptr("functionapp"),
			SKU:      &armappservice.SKUDescription{Name: to.Ptr("Y1"), Tier: to.Ptr("Dynamic")},
		}, nil)
		if err == nil {
			_, err = planPoller.PollUntilDone(ctx, nil)
		}
		if err != nil {
			resp.Diagnostics.AddError("azure plan", err.Error())
			return
		}
		planID := "/subscriptions/" + r.azureSub + "/resourceGroups/" + rgName + "/providers/Microsoft.Web/serverfarms/" + planName
		sitePoller, err := r.azureWeb.BeginCreateOrUpdate(ctx, rgName, plan.Name.ValueString(), armappservice.Site{
			Location: &r.azureLoc,
			Kind:     to.Ptr("functionapp"),
			Properties: &armappservice.SiteProperties{
				ServerFarmID: &planID,
			},
		}, nil)
		if err == nil {
			_, err = sitePoller.PollUntilDone(ctx, nil)
		}
		if err != nil {
			resp.Diagnostics.AddError("azure function", err.Error())
			return
		}
               resp.State.Set(ctx, map[string]interface{}{
                        "id":             plan.Name.ValueString(),
                        "name":           plan.Name.ValueString(),
                        "type":           plan.Type.ValueString(),
                        "region":         r.azureLoc,
                        "runtime":        plan.Runtime.ValueString(),
                        "handler":        plan.Handler.ValueString(),
                        "code":           plan.Code.ValueString(),
                        "account":        acctName,
                        "plan":           planName,
                        "resource_group": rgName,
               })
       case "gcp":
               if r.gcpFunc == nil {
                       resp.Diagnostics.AddError("gcp", "missing client")
                       return
               }
               region := plan.Region.ValueString()
               if region == "" {
                       region = r.gcpRegion
               }
               name := plan.Name.ValueString()
               parent := "projects/" + r.gcpProj + "/locations/" + region
               codeBytes, err := ioutil.ReadFile(plan.Code.ValueString())
               if err != nil {
                       resp.Diagnostics.AddError("read code", err.Error())
                       return
               }
               urlResp, err := r.gcpFunc.Projects.Locations.Functions.GenerateUploadUrl(parent, &cloudfunctions.GenerateUploadUrlRequest{}).Context(ctx).Do()
               if err != nil {
                       resp.Diagnostics.AddError("gcp generate url", err.Error())
                       return
               }
               reqUpload, err := http.NewRequestWithContext(ctx, http.MethodPut, urlResp.UploadUrl, strings.NewReader(string(codeBytes)))
               if err == nil {
                       reqUpload.Header.Set("Content-Type", "application/zip")
                       _, err = http.DefaultClient.Do(reqUpload)
               }
               if err != nil {
                       resp.Diagnostics.AddError("gcp upload", err.Error())
                       return
               }
               cf := &cloudfunctions.CloudFunction{
                       Name:          parent + "/functions/" + name,
                       EntryPoint:    plan.Handler.ValueString(),
                       Runtime:       plan.Runtime.ValueString(),
                       SourceUploadUrl: urlResp.UploadUrl,
                       HttpsTrigger: &cloudfunctions.HttpsTrigger{},
               }
               op, err := r.gcpFunc.Projects.Locations.Functions.Create(parent, cf).Context(ctx).Do()
               if err != nil {
                       resp.Diagnostics.AddError("gcp create", err.Error())
                       return
               }
               for {
                       oper, err := r.gcpFunc.Operations.Get(op.Name).Context(ctx).Do()
                       if err != nil {
                               resp.Diagnostics.AddError("gcp create", err.Error())
                               return
                       }
                       if oper.Done {
                               break
                       }
                       time.Sleep(5 * time.Second)
               }
               resp.State.Set(ctx, map[string]interface{}{
                       "id":      name,
                       "name":    name,
                       "type":    plan.Type.ValueString(),
                       "region":  region,
                       "runtime": plan.Runtime.ValueString(),
                       "handler": plan.Handler.ValueString(),
                       "code":    plan.Code.ValueString(),
               })
       default:
               resp.Diagnostics.AddError("unsupported cloud", "only aws and azure implemented")
       }
}

func (r *FunctionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state struct {
		ID            types.String `tfsdk:"id"`
		Type          types.String `tfsdk:"type"`
		Plan          types.String `tfsdk:"plan"`
		ResourceGroup types.String `tfsdk:"resource_group"`
	}
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	switch state.Type.ValueString() {
	case "aws":
		if r.lambda == nil {
			return
		}
		_, err := r.lambda.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(state.ID.ValueString())})
		if err != nil {
			resp.State.RemoveResource(ctx)
		}
       case "azure":
               if r.azureWeb == nil {
                       return
               }
               _, err := r.azureWeb.Get(ctx, "abstract-rg", state.ID.ValueString(), nil)
               if err != nil {
                       resp.State.RemoveResource(ctx)
               }
       case "gcp":
               if r.gcpFunc == nil {
                       return
               }
               region := r.gcpRegion
               if region == "" {
                       region = "us-central1"
               }
               _, err := r.gcpFunc.Projects.Locations.Functions.Get("projects/" + r.gcpProj + "/locations/" + region + "/functions/" + state.ID.ValueString()).Context(ctx).Do()
               if err != nil {
                       resp.State.RemoveResource(ctx)
               }
       }
}
func (r *FunctionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
}
func (r *FunctionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state struct {
		ID            types.String `tfsdk:"id"`
		Type          types.String `tfsdk:"type"`
		Plan          types.String `tfsdk:"plan"`
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
		if r.lambda == nil {
			return
		}
		_, err := r.lambda.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(state.ID.ValueString())})
		if err != nil {
			resp.Diagnostics.AddError("aws delete", err.Error())
		}
       case "azure":
               if r.azureWeb == nil {
                       return
               }
               _, err := r.azureWeb.Delete(ctx, "abstract-rg", state.ID.ValueString(), nil)
               if err != nil {
                       resp.Diagnostics.AddError("azure delete", err.Error())
               }
               if r.azurePlan != nil && state.Plan.ValueString() != "" {
                       _, _ = r.azurePlan.Delete(ctx, "abstract-rg", state.Plan.ValueString(), nil)
               }
               if r.azureAcct != nil && state.ResourceGroup.ValueString() != "" && state.Account.ValueString() != "" {
                       _, _ = r.azureAcct.Delete(ctx, state.ResourceGroup.ValueString(), state.Account.ValueString(), nil)
               }
       case "gcp":
               if r.gcpFunc == nil {
                       return
               }
               region := r.gcpRegion
               if region == "" {
                       region = "us-central1"
               }
               op, err := r.gcpFunc.Projects.Locations.Functions.Delete("projects/" + r.gcpProj + "/locations/" + region + "/functions/" + state.ID.ValueString()).Context(ctx).Do()
               if err != nil {
                       resp.Diagnostics.AddError("gcp delete", err.Error())
                       return
               }
               for {
                       oper, err := r.gcpFunc.Operations.Get(op.Name).Context(ctx).Do()
                       if err != nil {
                               resp.Diagnostics.AddError("gcp delete", err.Error())
                               return
                       }
                       if oper.Done {
                               break
                       }
                       time.Sleep(5 * time.Second)
               }
       }
}
