package resources

import (
	"context"
	"fmt"

	"abstract-provider/provider/shared"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	compute "google.golang.org/api/compute/v1"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	schema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type NetworkResource struct {
	ec2       *ec2.Client
	azureV    *armnetwork.VirtualNetworksClient
	azureS    *armnetwork.SubnetsClient
	azureRG   *armresources.ResourceGroupsClient
	azureCred azcore.TokenCredential
	azureLoc  string
	gcp       *compute.Service
	gcpProj   string
	gcpRegion string
}

func NewNetworkResource() resource.Resource { return &NetworkResource{} }

func (r *NetworkResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	cfg, ok := req.ProviderData.(*shared.ProviderConfig)
	if !ok {
		resp.Diagnostics.AddError("invalid provider data", "")
		return
	}
	r.ec2 = cfg.AWSEC2
	r.azureV = cfg.AzureVNetClient
	r.azureS = cfg.AzureSubnetClient
	r.azureRG = cfg.AzureRGClient
	r.azureCred = cfg.AzureCred
	r.azureLoc = cfg.AzureLocation
	r.gcp = cfg.GCPCompute
	r.gcpProj = cfg.GCPProject
	r.gcpRegion = cfg.GCPRegion
}

func (r *NetworkResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "abstract_network"
}

func (r *NetworkResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{Computed: true},
			"name":       schema.StringAttribute{Optional: true},
			"cidr":       schema.StringAttribute{Optional: true},
			"type":       schema.StringAttribute{Required: true},
			"subnet_id":  schema.StringAttribute{Computed: true},
			"gateway_id": schema.StringAttribute{Computed: true},
		},
	}
}

func (r *NetworkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan struct {
		Name types.String `tfsdk:"name"`
		CIDR types.String `tfsdk:"cidr"`
		Type types.String `tfsdk:"type"`
	}
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	switch plan.Type.ValueString() {
	case "aws":
		if r.ec2 == nil {
			resp.Diagnostics.AddError("missing AWS client", "")
			return
		}

		cidr := plan.CIDR.ValueString()
		if cidr == "" {
			cidr = "10.0.0.0/16"
		}
		vpcOut, err := r.ec2.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String(cidr)})
		if err != nil {
			resp.Diagnostics.AddError("aws create vpc", err.Error())
			return
		}
		vpcID := aws.ToString(vpcOut.Vpc.VpcId)

		if plan.Name.ValueString() != "" {
			_, err = r.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
				Resources: []string{vpcID},
				Tags:      []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String(plan.Name.ValueString())}},
			})
			if err != nil {
				resp.Diagnostics.AddError("aws tag vpc", err.Error())
				return
			}
		}

		azs, err := r.ec2.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{})
		if err != nil || len(azs.AvailabilityZones) == 0 {
			resp.Diagnostics.AddError("aws zones", "unable to determine availability zone")
			return
		}
		zone := aws.ToString(azs.AvailabilityZones[0].ZoneName)
		subnetOut, err := r.ec2.CreateSubnet(ctx, &ec2.CreateSubnetInput{
			VpcId:            aws.String(vpcID),
			CidrBlock:        aws.String(cidr),
			AvailabilityZone: aws.String(zone),
		})
		if err != nil {
			resp.Diagnostics.AddError("aws create subnet", err.Error())
			return
		}
		subnetID := aws.ToString(subnetOut.Subnet.SubnetId)

		igwOut, err := r.ec2.CreateInternetGateway(ctx, &ec2.CreateInternetGatewayInput{})
		if err != nil {
			resp.Diagnostics.AddError("aws create igw", err.Error())
			return
		}
		gatewayID := aws.ToString(igwOut.InternetGateway.InternetGatewayId)
		_, err = r.ec2.AttachInternetGateway(ctx, &ec2.AttachInternetGatewayInput{
			VpcId:             aws.String(vpcID),
			InternetGatewayId: aws.String(gatewayID),
		})
		if err != nil {
			resp.Diagnostics.AddError("aws attach igw", err.Error())
			return
		}

		resp.State.Set(ctx, map[string]interface{}{
			"id":         vpcID,
			"name":       plan.Name.ValueString(),
			"cidr":       cidr,
			"type":       plan.Type.ValueString(),
			"subnet_id":  subnetID,
			"gateway_id": gatewayID,
		})
		return
	case "azure":
		if r.azureV == nil || r.azureS == nil || r.azureRG == nil {
			resp.Diagnostics.AddError("azure", "missing client")
			return
		}

		cidr := plan.CIDR.ValueString()
		if cidr == "" {
			cidr = "10.0.0.0/16"
		}
		rgName := "abstract-rg"
		if r.azureLoc == "" {
			r.azureLoc = "eastus"
		}
		_, err := r.azureRG.CreateOrUpdate(ctx, rgName, armresources.ResourceGroup{Location: &r.azureLoc}, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure rg", err.Error())
			return
		}
		vnetPoller, err := r.azureV.BeginCreateOrUpdate(ctx, rgName, plan.Name.ValueString(), armnetwork.VirtualNetwork{
			Location: &r.azureLoc,
			Properties: &armnetwork.VirtualNetworkPropertiesFormat{
				AddressSpace: &armnetwork.AddressSpace{AddressPrefixes: []*string{&cidr}},
			},
		}, nil)
		var vnetID string
		if err == nil {
			vnetResp, perr := vnetPoller.PollUntilDone(ctx, nil)
			err = perr
			if perr == nil && vnetResp.ID != nil {
				vnetID = *vnetResp.ID
			}
		}
		if err != nil {
			resp.Diagnostics.AddError("azure create vnet", err.Error())
			return
		}
		subnetPoller, err := r.azureS.BeginCreateOrUpdate(ctx, rgName, plan.Name.ValueString(), "default", armnetwork.Subnet{
			Properties: &armnetwork.SubnetPropertiesFormat{AddressPrefix: &cidr},
		}, nil)
		var subnetID string
		if err == nil {
			subnetResp, serr := subnetPoller.PollUntilDone(ctx, nil)
			err = serr
			if serr == nil && subnetResp.ID != nil {
				subnetID = *subnetResp.ID
			}
		}
		if err != nil {
			resp.Diagnostics.AddError("azure create subnet", err.Error())
			return
		}

		resp.State.Set(ctx, map[string]interface{}{
			"id":        vnetID,
			"name":      plan.Name.ValueString(),
			"cidr":      cidr,
			"type":      plan.Type.ValueString(),
			"subnet_id": subnetID,
		})
		return
	case "gcp":
		if r.gcp == nil {
			resp.Diagnostics.AddError("gcp", "missing client")
			return
		}
		name := plan.Name.ValueString()
		if name == "" {
			name = "abstract-network"
		}
		net := &compute.Network{Name: name}
		cidr := plan.CIDR.ValueString()
		if cidr == "" {
			net.AutoCreateSubnetworks = true
		} else {
			net.AutoCreateSubnetworks = false
		}
		_, err := r.gcp.Networks.Insert(r.gcpProj, net).Context(ctx).Do()
		if err != nil {
			resp.Diagnostics.AddError("gcp create network", err.Error())
			return
		}
		var subnetID string
		if cidr != "" {
			sn := &compute.Subnetwork{
				Name:        name + "-subnet",
				IpCidrRange: cidr,
				Network:     fmt.Sprintf("projects/%s/global/networks/%s", r.gcpProj, name),
			}
			region := r.gcpRegion
			if region == "" {
				region = "us-central1"
			}
			_, err = r.gcp.Subnetworks.Insert(r.gcpProj, region, sn).Context(ctx).Do()
			if err != nil {
				resp.Diagnostics.AddError("gcp create subnet", err.Error())
				return
			}
			subnetID = sn.Name
		}
		resp.State.Set(ctx, map[string]interface{}{
			"id":        name,
			"name":      name,
			"cidr":      cidr,
			"type":      plan.Type.ValueString(),
			"subnet_id": subnetID,
		})
		return
	default:
		resp.Diagnostics.AddError("unsupported cloud", "only aws and azure implemented")
		return
	}
}

func (r *NetworkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
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
		if r.ec2 == nil {
			return
		}
		out, err := r.ec2.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{state.ID.ValueString()}})
		if err != nil || len(out.Vpcs) == 0 {
			resp.State.RemoveResource(ctx)
		}
	case "azure":
		if r.azureV == nil {
			return
		}
		_, err := r.azureV.Get(ctx, "abstract-rg", state.ID.ValueString(), nil)
		if err != nil {
			resp.State.RemoveResource(ctx)
		}
	case "gcp":
		if r.gcp == nil {
			return
		}
		_, err := r.gcp.Networks.Get(r.gcpProj, state.ID.ValueString()).Context(ctx).Do()
		if err != nil {
			resp.State.RemoveResource(ctx)
		}
	}
}

func (r *NetworkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
}

func (r *NetworkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state struct {
		ID        types.String `tfsdk:"id"`
		Type      types.String `tfsdk:"type"`
		SubnetID  types.String `tfsdk:"subnet_id"`
		GatewayID types.String `tfsdk:"gateway_id"`
	}
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	switch state.Type.ValueString() {
	case "aws":
		if r.ec2 == nil {
			return
		}
		if state.GatewayID.ValueString() != "" {
			_, _ = r.ec2.DetachInternetGateway(ctx, &ec2.DetachInternetGatewayInput{
				InternetGatewayId: aws.String(state.GatewayID.ValueString()),
				VpcId:             aws.String(state.ID.ValueString()),
			})
			_, _ = r.ec2.DeleteInternetGateway(ctx, &ec2.DeleteInternetGatewayInput{InternetGatewayId: aws.String(state.GatewayID.ValueString())})
		}
		if state.SubnetID.ValueString() != "" {
			_, _ = r.ec2.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: aws.String(state.SubnetID.ValueString())})
		}
		_, err := r.ec2.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: aws.String(state.ID.ValueString())})
		if err != nil {
			resp.Diagnostics.AddError("aws delete vpc", err.Error())
		}
	case "azure":
		if r.azureV == nil {
			return
		}
		poller, err := r.azureV.BeginDelete(ctx, "abstract-rg", state.ID.ValueString(), nil)
		if err == nil {
			_, err = poller.PollUntilDone(ctx, nil)
		}
		if err != nil {
			resp.Diagnostics.AddError("azure delete vnet", err.Error())
		}
	case "gcp":
		if r.gcp == nil {
			return
		}
		if state.SubnetID.ValueString() != "" {
			_, _ = r.gcp.Subnetworks.Delete(r.gcpProj, r.gcpRegion, state.SubnetID.ValueString()).Context(ctx).Do()
		}
		_, err := r.gcp.Networks.Delete(r.gcpProj, state.ID.ValueString()).Context(ctx).Do()
		if err != nil {
			resp.Diagnostics.AddError("gcp delete network", err.Error())
		}
	}
}
