package resources

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"abstract-provider/provider/shared"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/mysql/armmysqlflexibleservers"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/postgresql/armpostgresqlflexibleservers"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/aws/aws-sdk-go-v2/aws"
       "github.com/aws/aws-sdk-go-v2/service/rds"
       sqladmin "google.golang.org/api/sqladmin/v1beta4"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	schema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type DatabaseResource struct {
        rds        *rds.Client
        azureMySQL *armmysqlflexibleservers.ServersClient
        azurePG    *armpostgresqlflexibleservers.ServersClient
        azureRG    *armresources.ResourceGroupsClient
        azureCred  azcore.TokenCredential
        azureSubID string
        azureLoc   string
       gcpSQL   *sqladmin.Service
       gcpProj  string
       gcpRegion string
}

func NewDatabaseResource() resource.Resource { return &DatabaseResource{} }

func (r *DatabaseResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	cfg, ok := req.ProviderData.(*shared.ProviderConfig)
	if !ok {
		resp.Diagnostics.AddError("invalid provider data", "")
		return
	}
	r.rds = cfg.AWSRDS
	r.azureMySQL = cfg.AzureMySQLClient
	r.azurePG = cfg.AzurePostgresClient
	r.azureRG = cfg.AzureRGClient
       r.azureCred = cfg.AzureCred
       r.azureSubID = cfg.AzureSubID
       r.azureLoc = cfg.AzureLocation
       r.gcpSQL = cfg.GCPCloudSQL
       r.gcpProj = cfg.GCPProject
       r.gcpRegion = cfg.GCPRegion
}

func (r *DatabaseResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "abstract_database"
}

func (r *DatabaseResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id":      schema.StringAttribute{Computed: true},
			"name":    schema.StringAttribute{Optional: true},
			"type":    schema.StringAttribute{Required: true},
			"engine":  schema.StringAttribute{Required: true},
			"version": schema.StringAttribute{Optional: true},
			"size":    schema.StringAttribute{Optional: true},
		},
	}
}

func (r *DatabaseResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan struct {
		Name    types.String `tfsdk:"name"`
		Type    types.String `tfsdk:"type"`
		Engine  types.String `tfsdk:"engine"`
		Version types.String `tfsdk:"version"`
		Size    types.String `tfsdk:"size"`
	}
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
       switch plan.Type.ValueString() {
       case "aws":
		if r.rds == nil {
			resp.Diagnostics.AddError("missing AWS client", "")
			return
		}
		id := plan.Name.ValueString()
		if id == "" {
			id = fmt.Sprintf("db-%d", time.Now().Unix())
		}
		class := plan.Size.ValueString()
		if class == "" {
			class = "db.t3.micro"
		}
		password := os.Getenv("RDS_PASSWORD")
		if password == "" {
			resp.Diagnostics.AddError("missing password", "RDS_PASSWORD must be set")
			return
		}
		input := &rds.CreateDBInstanceInput{
			DBInstanceIdentifier: aws.String(id),
			Engine:               aws.String(plan.Engine.ValueString()),
			DBInstanceClass:      aws.String(class),
			MasterUsername:       aws.String("admin"),
			MasterUserPassword:   aws.String(password),
			AllocatedStorage:     aws.Int32(20),
			PubliclyAccessible:   aws.Bool(false),
		}
		if plan.Version.ValueString() != "" {
			input.EngineVersion = aws.String(plan.Version.ValueString())
		}
		_, err := r.rds.CreateDBInstance(ctx, input)
		if err != nil {
			resp.Diagnostics.AddError("aws create", err.Error())
			return
		}
		resp.State.Set(ctx, map[string]interface{}{
			"id":      id,
			"name":    plan.Name.ValueString(),
			"type":    plan.Type.ValueString(),
			"engine":  plan.Engine.ValueString(),
			"version": plan.Version.ValueString(),
			"size":    class,
		})
       case "azure":
		if r.azureMySQL == nil || r.azurePG == nil || r.azureRG == nil {
			resp.Diagnostics.AddError("azure", "missing client")
			return
		}
		rgName := "abstract-rg"
		if r.azureLoc == "" && plan.Size.ValueString() != "" {
			r.azureLoc = plan.Size.ValueString()
		}
		_, err := r.azureRG.CreateOrUpdate(ctx, rgName, armresources.ResourceGroup{Location: &r.azureLoc}, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure rg", err.Error())
			return
		}
		name := plan.Name.ValueString()
		if name == "" {
			name = fmt.Sprintf("db-%d", time.Now().Unix())
		}
		password := os.Getenv("AZURE_DB_PASSWORD")
		if password == "" {
			resp.Diagnostics.AddError("missing password", "AZURE_DB_PASSWORD must be set")
			return
		}
		engine := strings.ToLower(plan.Engine.ValueString())
		size := plan.Size.ValueString()
		if size == "" {
			size = "Standard_B1ms"
		}
		switch engine {
		case "mysql":
			poller, err := r.azureMySQL.BeginCreate(ctx, rgName, name, armmysqlflexibleservers.Server{
				Location: &r.azureLoc,
				Properties: &armmysqlflexibleservers.ServerProperties{
					AdministratorLogin:         to.Ptr("adminuser"),
					AdministratorLoginPassword: to.Ptr(password),
				},
			}, nil)
			if err == nil {
				_, err = poller.PollUntilDone(ctx, nil)
			}
			if err != nil {
				resp.Diagnostics.AddError("azure create", err.Error())
				return
			}
		case "postgresql", "postgres":
			poller, err := r.azurePG.BeginCreate(ctx, rgName, name, armpostgresqlflexibleservers.Server{
				Location: &r.azureLoc,
				Properties: &armpostgresqlflexibleservers.ServerProperties{
					AdministratorLogin:         to.Ptr("adminuser"),
					AdministratorLoginPassword: to.Ptr(password),
				},
				SKU: &armpostgresqlflexibleservers.SKU{Name: to.Ptr(size)},
			}, nil)
			if err == nil {
				_, err = poller.PollUntilDone(ctx, nil)
			}
			if err != nil {
				resp.Diagnostics.AddError("azure create", err.Error())
				return
			}
		default:
			resp.Diagnostics.AddError("unsupported engine", engine)
			return
		}
		resp.State.Set(ctx, map[string]interface{}{
			"id":      name,
			"name":    plan.Name.ValueString(),
			"type":    plan.Type.ValueString(),
			"engine":  plan.Engine.ValueString(),
			"version": plan.Version.ValueString(),
			"size":    size,
		})
       case "gcp":
               if r.gcpSQL == nil {
                       resp.Diagnostics.AddError("gcp", "missing client")
                       return
               }
               name := plan.Name.ValueString()
               if name == "" {
                       name = fmt.Sprintf("db-%d", time.Now().Unix())
               }
               region := r.gcpRegion
               if region == "" {
                       region = "us-central1"
               }
               tier := plan.Size.ValueString()
               if tier == "" {
                       tier = "db-f1-micro"
               }
               engine := strings.ToLower(plan.Engine.ValueString())
               version := plan.Version.ValueString()
               if version == "" {
                       if engine == "postgres" || engine == "postgresql" {
                               version = "POSTGRES_15"
                       } else {
                               version = "MYSQL_8_0"
                       }
               }
               inst := &sqladmin.DatabaseInstance{
                       Name:           name,
                       Region:         region,
                       DatabaseVersion: version,
                       Settings:       &sqladmin.Settings{Tier: tier},
               }
               op, err := r.gcpSQL.Instances.Insert(r.gcpProj, inst).Context(ctx).Do()
               if err != nil {
                       resp.Diagnostics.AddError("gcp create", err.Error())
                       return
               }
               for {
                       oper, err := r.gcpSQL.Operations.Get(r.gcpProj, op.Name).Context(ctx).Do()
                       if err != nil {
                               resp.Diagnostics.AddError("gcp create", err.Error())
                               return
                       }
                       if oper.Status == "DONE" {
                               break
                       }
                       time.Sleep(5 * time.Second)
               }
               resp.State.Set(ctx, map[string]interface{}{
                       "id":      name,
                       "name":    plan.Name.ValueString(),
                       "type":    plan.Type.ValueString(),
                       "engine":  plan.Engine.ValueString(),
                       "version": version,
                       "size":    tier,
               })
       default:
               resp.Diagnostics.AddError("unsupported cloud", "only aws, azure, and gcp implemented")
               return
       }
}

func (r *DatabaseResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state struct {
		ID   types.String `tfsdk:"id"`
		Type types.String `tfsdk:"type"`
	}
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	switch state.Type.ValueString() {
	case "aws":
		if r.rds == nil {
			return
		}
		_, err := r.rds.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(state.ID.ValueString())})
		if err != nil {
			resp.State.RemoveResource(ctx)
		}
       case "azure":
               if r.azureMySQL == nil || r.azurePG == nil {
                       return
               }
               _, err := r.azureMySQL.Get(ctx, "abstract-rg", state.ID.ValueString(), nil)
               if err != nil {
                       _, err2 := r.azurePG.Get(ctx, "abstract-rg", state.ID.ValueString(), nil)
                       if err2 != nil {
                               resp.State.RemoveResource(ctx)
                       }
               }
       case "gcp":
               if r.gcpSQL == nil {
                       return
               }
               _, err := r.gcpSQL.Instances.Get(r.gcpProj, state.ID.ValueString()).Context(ctx).Do()
               if err != nil {
                       resp.State.RemoveResource(ctx)
               }
       }
}
func (r *DatabaseResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
}
func (r *DatabaseResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state struct {
		ID   types.String `tfsdk:"id"`
		Type types.String `tfsdk:"type"`
	}
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	switch state.Type.ValueString() {
	case "aws":
		if r.rds == nil {
			return
		}
		_, err := r.rds.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{DBInstanceIdentifier: aws.String(state.ID.ValueString()), SkipFinalSnapshot: true})
		if err != nil {
			resp.Diagnostics.AddError("aws delete", err.Error())
		}
       case "azure":
               if r.azureMySQL == nil || r.azurePG == nil {
                       return
               }
               poller, err := r.azureMySQL.BeginDelete(ctx, "abstract-rg", state.ID.ValueString(), nil)
               if err == nil {
                       _, err = poller.PollUntilDone(ctx, nil)
               }
               if err != nil {
                       poller2, err2 := r.azurePG.BeginDelete(ctx, "abstract-rg", state.ID.ValueString(), nil)
                       if err2 == nil {
                               _, err2 = poller2.PollUntilDone(ctx, nil)
                       }
                       if err2 != nil {
                               resp.Diagnostics.AddError("azure delete", err2.Error())
                       }
               }
       case "gcp":
               if r.gcpSQL == nil {
                       return
               }
               op, err := r.gcpSQL.Instances.Delete(r.gcpProj, state.ID.ValueString()).Context(ctx).Do()
               if err != nil {
                       resp.Diagnostics.AddError("gcp delete", err.Error())
                       return
               }
               for {
                       oper, err := r.gcpSQL.Operations.Get(r.gcpProj, op.Name).Context(ctx).Do()
                       if err != nil {
                               resp.Diagnostics.AddError("gcp delete", err.Error())
                               return
                       }
                       if oper.Status == "DONE" {
                               break
                       }
                       time.Sleep(5 * time.Second)
               }
       }
}
