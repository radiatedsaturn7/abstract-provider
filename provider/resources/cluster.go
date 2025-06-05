package resources

import (
	"context"
	"fmt"
	"os"
	"time"

	"abstract-provider/provider/shared"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	schema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	container "google.golang.org/api/container/v1"
)

type ClusterResource struct {
	eks *eks.Client
	ec2 *ec2.Client

	azureAKS  *armcontainerservice.ManagedClustersClient
	azureRG   *armresources.ResourceGroupsClient
	azureCred azcore.TokenCredential
	azureLoc  string

	gke       *container.Service
	gcpProj   string
	gcpRegion string
}

func NewClusterResource() resource.Resource { return &ClusterResource{} }

func (r *ClusterResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	cfg, ok := req.ProviderData.(*shared.ProviderConfig)
	if !ok {
		resp.Diagnostics.AddError("invalid provider data", "")
		return
	}
	r.eks = cfg.AWSEKS
	r.ec2 = cfg.AWSEC2
	r.azureAKS = cfg.AzureAKSClient
	r.azureRG = cfg.AzureRGClient
	r.azureCred = cfg.AzureCred
	r.azureLoc = cfg.AzureLocation
	r.gke = cfg.GCPGKE
	r.gcpProj = cfg.GCPProject
	r.gcpRegion = cfg.GCPRegion
}

func (r *ClusterResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "abstract_cluster"
}

func (r *ClusterResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{Computed: true},
			"name":       schema.StringAttribute{Optional: true},
			"type":       schema.StringAttribute{Required: true},
			"region":     schema.StringAttribute{Optional: true},
			"node_count": schema.Int64Attribute{Optional: true},
			"node_size":  schema.StringAttribute{Optional: true},
		},
	}
}

func (r *ClusterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan struct {
		Name      types.String `tfsdk:"name"`
		Type      types.String `tfsdk:"type"`
		Region    types.String `tfsdk:"region"`
		NodeCount types.Int64  `tfsdk:"node_count"`
		NodeSize  types.String `tfsdk:"node_size"`
	}
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	switch plan.Type.ValueString() {
	case "aws":
		if r.eks == nil || r.ec2 == nil {
			resp.Diagnostics.AddError("missing AWS client", "")
			return
		}

		// determine subnets from default VPC
		vpcs, err := r.ec2.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{Filters: []ec2types.Filter{{Name: aws.String("isDefault"), Values: []string{"true"}}}})
		if err != nil || len(vpcs.Vpcs) == 0 {
			resp.Diagnostics.AddError("aws default vpc", "unable to find default vpc")
			return
		}
		vpcID := aws.ToString(vpcs.Vpcs[0].VpcId)
		subnetsOut, err := r.ec2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{Filters: []ec2types.Filter{{Name: aws.String("vpc-id"), Values: []string{vpcID}}}})
		if err != nil || len(subnetsOut.Subnets) == 0 {
			resp.Diagnostics.AddError("aws subnets", "unable to find subnets in default vpc")
			return
		}
		subnetIDs := []string{}
		for i, s := range subnetsOut.Subnets {
			if i >= 2 {
				break
			}
			subnetIDs = append(subnetIDs, aws.ToString(s.SubnetId))
		}
		role := os.Getenv("EKS_ROLE_ARN")
		nodeRole := os.Getenv("EKS_NODE_ROLE_ARN")
		if role == "" || nodeRole == "" {
			resp.Diagnostics.AddError("missing roles", "EKS_ROLE_ARN and EKS_NODE_ROLE_ARN must be set")
			return
		}
		_, err = r.eks.CreateCluster(ctx, &eks.CreateClusterInput{
			Name:    aws.String(plan.Name.ValueString()),
			RoleArn: aws.String(role),
			ResourcesVpcConfig: &ekstypes.VpcConfigRequest{
				SubnetIds: subnetIDs,
			},
		})
		if err != nil {
			resp.Diagnostics.AddError("aws create cluster", err.Error())
			return
		}
		desired := int32(3)
		if plan.NodeCount.ValueInt64() > 0 {
			desired = int32(plan.NodeCount.ValueInt64())
		}
		instanceType := plan.NodeSize.ValueString()
		if instanceType == "" {
			instanceType = "t3.medium"
		}
		_, err = r.eks.CreateNodegroup(ctx, &eks.CreateNodegroupInput{
			ClusterName:   aws.String(plan.Name.ValueString()),
			NodegroupName: aws.String(plan.Name.ValueString() + "-ng"),
			NodeRole:      aws.String(nodeRole),
			Subnets:       subnetIDs,
			ScalingConfig: &ekstypes.NodegroupScalingConfig{DesiredSize: aws.Int32(desired), MinSize: aws.Int32(desired), MaxSize: aws.Int32(desired)},
			InstanceTypes: []string{instanceType},
		})
		if err != nil {
			resp.Diagnostics.AddError("aws create nodegroup", err.Error())
			return
		}

		resp.State.Set(ctx, map[string]interface{}{
			"id":         plan.Name.ValueString(),
			"name":       plan.Name.ValueString(),
			"type":       plan.Type.ValueString(),
			"region":     plan.Region.ValueString(),
			"node_count": int64(desired),
			"node_size":  instanceType,
		})
	case "azure":
		if r.azureAKS == nil || r.azureRG == nil {
			resp.Diagnostics.AddError("azure", "missing client")
			return
		}
		if r.azureLoc == "" {
			r.azureLoc = plan.Region.ValueString()
			if r.azureLoc == "" {
				r.azureLoc = "eastus"
			}
		}
		rgName := "abstract-rg"
		_, err := r.azureRG.CreateOrUpdate(ctx, rgName, armresources.ResourceGroup{Location: &r.azureLoc}, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure rg", err.Error())
			return
		}
		nodeCount := int32(3)
		if plan.NodeCount.ValueInt64() > 0 {
			nodeCount = int32(plan.NodeCount.ValueInt64())
		}
		vmSize := plan.NodeSize.ValueString()
		if vmSize == "" {
			vmSize = "Standard_DS2_v2"
		}
		name := plan.Name.ValueString()
		poller, err := r.azureAKS.BeginCreateOrUpdate(ctx, rgName, name, armcontainerservice.ManagedCluster{
			Location: &r.azureLoc,
			Properties: &armcontainerservice.ManagedClusterProperties{
				DNSPrefix: &name,
				AgentPoolProfiles: []*armcontainerservice.ManagedClusterAgentPoolProfile{{
					Name:   to.Ptr("nodepool1"),
					Count:  &nodeCount,
					VMSize: &vmSize,
				}},
			},
		}, nil)
		if err == nil {
			_, err = poller.PollUntilDone(ctx, nil)
		}
		if err != nil {
			resp.Diagnostics.AddError("azure create aks", err.Error())
			return
		}

		resp.State.Set(ctx, map[string]interface{}{
			"id":         plan.Name.ValueString(),
			"name":       plan.Name.ValueString(),
			"type":       plan.Type.ValueString(),
			"region":     r.azureLoc,
			"node_count": int64(nodeCount),
			"node_size":  vmSize,
		})
	case "gcp":
		if r.gke == nil {
			resp.Diagnostics.AddError("gcp", "missing client")
			return
		}
		region := plan.Region.ValueString()
		if region == "" {
			region = r.gcpRegion
			if region == "" {
				region = "us-central1"
			}
		}
		name := plan.Name.ValueString()
		if name == "" {
			name = "abstract-cluster"
		}
		count := int64(3)
		if plan.NodeCount.ValueInt64() > 0 {
			count = plan.NodeCount.ValueInt64()
		}
		machine := plan.NodeSize.ValueString()
		if machine == "" {
			machine = "e2-medium"
		}
		parent := fmt.Sprintf("projects/%s/locations/%s", r.gcpProj, region)
		cluster := &container.Cluster{
			Name:             name,
			InitialNodeCount: count,
			NodeConfig: &container.NodeConfig{
				MachineType: machine,
			},
		}
		op, err := r.gke.Projects.Locations.Clusters.Create(parent, cluster).Context(ctx).Do()
		if err != nil {
			resp.Diagnostics.AddError("gcp create cluster", err.Error())
			return
		}
		for {
			oper, err := r.gke.Projects.Locations.Operations.Get(op.Name).Context(ctx).Do()
			if err != nil {
				resp.Diagnostics.AddError("gcp create cluster", err.Error())
				return
			}
			if oper.Status == "DONE" {
				break
			}
			time.Sleep(5 * time.Second)
		}
		resp.State.Set(ctx, map[string]interface{}{
			"id":         name,
			"name":       name,
			"type":       plan.Type.ValueString(),
			"region":     region,
			"node_count": count,
			"node_size":  machine,
		})
	default:
		resp.Diagnostics.AddError("unsupported cloud", "only aws, azure, and gcp implemented")
	}
}

func (r *ClusterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
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
		if r.eks == nil {
			return
		}
		_, err := r.eks.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(state.ID.ValueString())})
		if err != nil {
			resp.State.RemoveResource(ctx)
		}
	case "azure":
		if r.azureAKS == nil {
			return
		}
		_, err := r.azureAKS.Get(ctx, "abstract-rg", state.ID.ValueString(), nil)
		if err != nil {
			resp.State.RemoveResource(ctx)
		}
	case "gcp":
		if r.gke == nil {
			return
		}
		region := r.gcpRegion
		if region == "" {
			region = "us-central1"
		}
		_, err := r.gke.Projects.Locations.Clusters.Get(fmt.Sprintf("projects/%s/locations/%s/clusters/%s", r.gcpProj, region, state.ID.ValueString())).Context(ctx).Do()
		if err != nil {
			resp.State.RemoveResource(ctx)
		}
	}
}
func (r *ClusterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
}
func (r *ClusterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
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
		if r.eks == nil {
			return
		}
		nodeGroup := state.ID.ValueString() + "-ng"
		_, _ = r.eks.DeleteNodegroup(ctx, &eks.DeleteNodegroupInput{ClusterName: aws.String(state.ID.ValueString()), NodegroupName: aws.String(nodeGroup)})
		_, err := r.eks.DeleteCluster(ctx, &eks.DeleteClusterInput{Name: aws.String(state.ID.ValueString())})
		if err != nil {
			resp.Diagnostics.AddError("aws delete", err.Error())
		}
	case "azure":
		if r.azureAKS == nil {
			return
		}
		poller, err := r.azureAKS.BeginDelete(ctx, "abstract-rg", state.ID.ValueString(), nil)
		if err == nil {
			_, err = poller.PollUntilDone(ctx, nil)
		}
		if err != nil {
			resp.Diagnostics.AddError("azure delete", err.Error())
		}
	case "gcp":
		if r.gke == nil {
			return
		}
		region := r.gcpRegion
		if region == "" {
			region = "us-central1"
		}
		op, err := r.gke.Projects.Locations.Clusters.Delete(fmt.Sprintf("projects/%s/locations/%s/clusters/%s", r.gcpProj, region, state.ID.ValueString())).Context(ctx).Do()
		if err != nil {
			resp.Diagnostics.AddError("gcp delete", err.Error())
			return
		}
		for {
			oper, err := r.gke.Projects.Locations.Operations.Get(op.Name).Context(ctx).Do()
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
