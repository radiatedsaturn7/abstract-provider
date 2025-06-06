package resources

import (
    "context"
    "fmt"

    "abstract-provider/provider/shared"
    "github.com/Azure/azure-sdk-for-go/sdk/azcore"
    "github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
    ci "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerinstance/armcontainerinstance"
    "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
    "github.com/aws/aws-sdk-go-v2/aws"
    "github.com/aws/aws-sdk-go-v2/service/ec2"
    "github.com/aws/aws-sdk-go-v2/service/ecs"
    ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
    "github.com/hashicorp/terraform-plugin-framework/resource"
    schema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
    "github.com/hashicorp/terraform-plugin-framework/types"
)

// ServerlessContainerResource manages a serverless container service.
type ServerlessContainerResource struct {
    ecs    *ecs.Client
    ec2    *ec2.Client
    azureRG *armresources.ResourceGroupsClient
    azureCI *ci.ContainerGroupsClient
    azureCred azcore.TokenCredential
    azureSubID string
    azureLoc   string
}

func NewServerlessContainerResource() resource.Resource { return &ServerlessContainerResource{} }

func (r *ServerlessContainerResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
    if req.ProviderData == nil {
        return
    }
    cfg, ok := req.ProviderData.(*shared.ProviderConfig)
    if !ok {
        resp.Diagnostics.AddError("invalid provider data", "")
        return
    }
    r.ecs = cfg.AWSECS
    r.ec2 = cfg.AWSEC2
    r.azureRG = cfg.AzureRGClient
    r.azureCI = cfg.AzureContainerClient
    r.azureCred = cfg.AzureCred
    r.azureSubID = cfg.AzureSubID
    r.azureLoc = cfg.AzureLocation
}

func (r *ServerlessContainerResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
    resp.TypeName = "abstract_container"
}

func (r *ServerlessContainerResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
    resp.Schema = schema.Schema{
        Attributes: map[string]schema.Attribute{
            "id":         schema.StringAttribute{Computed: true},
            "name":       schema.StringAttribute{Required: true},
            "image":      schema.StringAttribute{Required: true},
            "type":       schema.StringAttribute{Required: true},
            "region":     schema.StringAttribute{Optional: true},
            "ip_address": schema.StringAttribute{Computed: true},
        },
    }
}

func (r *ServerlessContainerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
    var plan struct {
        Name   types.String `tfsdk:"name"`
        Image  types.String `tfsdk:"image"`
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
        if r.ecs == nil || r.ec2 == nil {
            resp.Diagnostics.AddError("aws", "missing client")
            return
        }
        subOut, err := r.ec2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{})
        if err != nil || len(subOut.Subnets) == 0 {
            resp.Diagnostics.AddError("aws subnets", "unable to find subnets")
            return
        }
        subnet := aws.ToString(subOut.Subnets[0].SubnetId)
        tdOut, err := r.ecs.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
            Family:                  aws.String(plan.Name.ValueString()),
            RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
            NetworkMode:             ecstypes.NetworkModeAwsvpc,
            Cpu:                     aws.String("256"),
            Memory:                  aws.String("512"),
            ContainerDefinitions: []ecstypes.ContainerDefinition{{
                Name:      aws.String("app"),
                Image:     aws.String(plan.Image.ValueString()),
                Essential: aws.Bool(true),
            }},
        })
        if err != nil {
            resp.Diagnostics.AddError("aws register", err.Error())
            return
        }
        tdArn := aws.ToString(tdOut.TaskDefinition.TaskDefinitionArn)
        runOut, err := r.ecs.RunTask(ctx, &ecs.RunTaskInput{
            Cluster:        aws.String("default"),
            LaunchType:     ecstypes.LaunchTypeFargate,
            TaskDefinition: aws.String(tdArn),
            NetworkConfiguration: &ecstypes.NetworkConfiguration{
                AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
                    Subnets:       []string{subnet},
                    AssignPublicIp: ecstypes.AssignPublicIpEnabled,
                },
            },
        })
        if err != nil || len(runOut.Tasks) == 0 {
            if err == nil {
                err = fmt.Errorf("no task returned")
            }
            resp.Diagnostics.AddError("aws run", err.Error())
            return
        }
        task := runOut.Tasks[0]
        resp.State.Set(ctx, map[string]interface{}{
            "id":    aws.ToString(task.TaskArn),
            "name":  plan.Name.ValueString(),
            "image": plan.Image.ValueString(),
            "type":  plan.Type.ValueString(),
        })
    case "azure":
        if r.azureCI == nil || r.azureRG == nil {
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
        poller, err := r.azureCI.BeginCreateOrUpdate(ctx, rgName, plan.Name.ValueString(), ci.ContainerGroup{
            Location: &r.azureLoc,
            Properties: &ci.ContainerGroupProperties{
                OsType:       to.Ptr(ci.OperatingSystemTypesLinux),
                RestartPolicy: to.Ptr(ci.ContainerGroupRestartPolicyNever),
                Containers: []*ci.Container{{
                    Name: to.Ptr(plan.Name.ValueString()),
                    Properties: &ci.ContainerProperties{
                        Image: to.Ptr(plan.Image.ValueString()),
                        Resources: &ci.ResourceRequirements{Requests: &ci.ResourceRequests{
                            CPU:        to.Ptr[float64](1.0),
                            MemoryInGB: to.Ptr[float64](1.0),
                        }},
                    },
                }},
                IPAddress: &ci.IPAddress{Type: to.Ptr(ci.ContainerGroupIPAddressTypePublic)},
            },
        }, nil)
        if err == nil {
            _, err = poller.PollUntilDone(ctx, nil)
        }
        if err != nil {
            resp.Diagnostics.AddError("azure create", err.Error())
            return
        }
        cg, err := r.azureCI.Get(ctx, rgName, plan.Name.ValueString(), nil)
        if err != nil {
            resp.Diagnostics.AddError("azure get", err.Error())
            return
        }
        ip := ""
        if cg.Properties != nil && cg.Properties.IPAddress != nil && cg.Properties.IPAddress.IP != nil {
            ip = *cg.Properties.IPAddress.IP
        }
        resp.State.Set(ctx, map[string]interface{}{
            "id":         *cg.ID,
            "name":       plan.Name.ValueString(),
            "image":      plan.Image.ValueString(),
            "type":       plan.Type.ValueString(),
            "region":     r.azureLoc,
            "ip_address": ip,
        })
    case "gcp":
        resp.Diagnostics.AddError("gcp", "serverless container resource not implemented")
    default:
        resp.Diagnostics.AddError("unsupported cloud", "only aws and azure implemented")
    }
}

func (r *ServerlessContainerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
    var state struct {
        ID   types.String `tfsdk:"id"`
        Type types.String `tfsdk:"type"`
        Name types.String `tfsdk:"name"`
    }
    diags := req.State.Get(ctx, &state)
    resp.Diagnostics.Append(diags...)
    if resp.Diagnostics.HasError() {
        return
    }
    switch state.Type.ValueString() {
    case "aws":
        if r.ecs == nil {
            return
        }
        _, err := r.ecs.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: aws.String("default"), Tasks: []string{state.ID.ValueString()}})
        if err != nil {
            resp.State.RemoveResource(ctx)
        }
    case "azure":
        if r.azureCI == nil {
            return
        }
        _, err := r.azureCI.Get(ctx, "abstract-rg", state.Name.ValueString(), nil)
        if err != nil {
            resp.State.RemoveResource(ctx)
        }
    }
}

func (r *ServerlessContainerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {}

func (r *ServerlessContainerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
    var state struct {
        ID   types.String `tfsdk:"id"`
        Type types.String `tfsdk:"type"`
        Name types.String `tfsdk:"name"`
    }
    diags := req.State.Get(ctx, &state)
    resp.Diagnostics.Append(diags...)
    if resp.Diagnostics.HasError() {
        return
    }
    switch state.Type.ValueString() {
    case "aws":
        if r.ecs == nil {
            return
        }
        _, err := r.ecs.StopTask(ctx, &ecs.StopTaskInput{Cluster: aws.String("default"), Task: aws.String(state.ID.ValueString())})
        if err != nil {
            resp.Diagnostics.AddError("aws delete", err.Error())
        }
    case "azure":
        if r.azureCI == nil {
            return
        }
        poller, err := r.azureCI.BeginDelete(ctx, "abstract-rg", state.Name.ValueString(), nil)
        if err == nil {
            _, err = poller.PollUntilDone(ctx, nil)
        }
        if err != nil {
            resp.Diagnostics.AddError("azure delete", err.Error())
        }
    }
}

