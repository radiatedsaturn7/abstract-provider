package resources

import (
	"context"
	"fmt"

	"abstract-provider/provider/shared"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	schema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// LoadBalancerResource manages an abstract load balancer across clouds.
type LoadBalancerResource struct {
	elb        *elbv2.Client
	ec2        *ec2.Client
	azureRG    *armresources.ResourceGroupsClient
	azureLB    *armnetwork.LoadBalancersClient
	azurePIP   *armnetwork.PublicIPAddressesClient
	azureCred  azcore.TokenCredential
	azureSubID string
	azureLoc   string
}

func NewLoadBalancerResource() resource.Resource { return &LoadBalancerResource{} }

func (r *LoadBalancerResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	cfg, ok := req.ProviderData.(*shared.ProviderConfig)
	if !ok {
		resp.Diagnostics.AddError("invalid provider data", "")
		return
	}
	r.elb = cfg.AWSELB
	r.ec2 = cfg.AWSEC2
	r.azureRG = cfg.AzureRGClient
	r.azureLB = cfg.AzureLBClient
	r.azurePIP = cfg.AzurePIPClient
	r.azureCred = cfg.AzureCred
	r.azureSubID = cfg.AzureSubID
	r.azureLoc = cfg.AzureLocation
}

func (r *LoadBalancerResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "abstract_load_balancer"
}

func (r *LoadBalancerResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{Computed: true},
			"name":       schema.StringAttribute{Required: true},
			"type":       schema.StringAttribute{Required: true},
			"region":     schema.StringAttribute{Optional: true},
			"ip_address": schema.StringAttribute{Computed: true},
		},
	}
}

func (r *LoadBalancerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
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
		if r.elb == nil || r.ec2 == nil {
			resp.Diagnostics.AddError("aws", "missing client")
			return
		}
		subOut, err := r.ec2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{})
		if err != nil || len(subOut.Subnets) == 0 {
			resp.Diagnostics.AddError("aws subnets", "unable to find subnets")
			return
		}
		var subnets []string
		for i, s := range subOut.Subnets {
			if i >= 2 {
				break
			}
			subnets = append(subnets, aws.ToString(s.SubnetId))
		}
		lbOut, err := r.elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
			Name:          aws.String(plan.Name.ValueString()),
			Subnets:       subnets,
			Type:          elbtypes.LoadBalancerTypeEnumNetwork,
			Scheme:        elbtypes.LoadBalancerSchemeEnumInternetFacing,
			IpAddressType: elbtypes.IpAddressTypeIpv4,
		})
		if err != nil || len(lbOut.LoadBalancers) == 0 {
			if err == nil {
				err = fmt.Errorf("no load balancer returned")
			}
			resp.Diagnostics.AddError("aws create", err.Error())
			return
		}
		lb := lbOut.LoadBalancers[0]
		resp.State.Set(ctx, map[string]interface{}{
			"id":         aws.ToString(lb.LoadBalancerArn),
			"name":       plan.Name.ValueString(),
			"type":       plan.Type.ValueString(),
			"ip_address": aws.ToString(lb.DNSName),
		})
	case "azure":
		if r.azureLB == nil || r.azureRG == nil || r.azurePIP == nil {
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
		pipName := plan.Name.ValueString() + "-pip"
		pipPoller, err := r.azurePIP.BeginCreateOrUpdate(ctx, rgName, pipName, armnetwork.PublicIPAddress{
			Location: &r.azureLoc,
			Properties: &armnetwork.PublicIPAddressPropertiesFormat{
				PublicIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodStatic),
			},
		}, nil)
		var pipID string
		if err == nil {
			pipResp, perr := pipPoller.PollUntilDone(ctx, nil)
			err = perr
			if perr == nil && pipResp.ID != nil {
				pipID = *pipResp.ID
			}
		}
		if err != nil {
			resp.Diagnostics.AddError("azure pip", err.Error())
			return
		}
		lbPoller, err := r.azureLB.BeginCreateOrUpdate(ctx, rgName, plan.Name.ValueString(), armnetwork.LoadBalancer{
			Location: &r.azureLoc,
			Properties: &armnetwork.LoadBalancerPropertiesFormat{
				FrontendIPConfigurations: []*armnetwork.FrontendIPConfiguration{{
					Name: to.Ptr("lbfe"),
					Properties: &armnetwork.FrontendIPConfigurationPropertiesFormat{
						PublicIPAddress: &armnetwork.PublicIPAddress{ID: &pipID},
					},
				}},
			},
		}, nil)
		if err == nil {
			_, err = lbPoller.PollUntilDone(ctx, nil)
		}
		if err != nil {
			resp.Diagnostics.AddError("azure create lb", err.Error())
			return
		}
		pip, err := r.azurePIP.Get(ctx, rgName, pipName, nil)
		if err != nil || pip.Properties == nil || pip.Properties.IPAddress == nil {
			resp.Diagnostics.AddError("azure pip", "unable to get IP")
			return
		}
		resp.State.Set(ctx, map[string]interface{}{
			"id":         plan.Name.ValueString(),
			"name":       plan.Name.ValueString(),
			"type":       plan.Type.ValueString(),
			"region":     r.azureLoc,
			"ip_address": *pip.Properties.IPAddress,
		})
	case "gcp":
		resp.Diagnostics.AddError("gcp", "load balancer resource not implemented")
	default:
		resp.Diagnostics.AddError("unsupported cloud", "only aws and azure implemented")
	}
}

func (r *LoadBalancerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
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
		if r.elb == nil {
			return
		}
		_, err := r.elb.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{state.ID.ValueString()}})
		if err != nil {
			resp.State.RemoveResource(ctx)
		}
	case "azure":
		if r.azureLB == nil {
			return
		}
		_, err := r.azureLB.Get(ctx, "abstract-rg", state.Name.ValueString(), nil)
		if err != nil {
			resp.State.RemoveResource(ctx)
		}
	}
}

func (r *LoadBalancerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// no updatable fields
}

func (r *LoadBalancerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
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
		if r.elb == nil {
			return
		}
		_, err := r.elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(state.ID.ValueString())})
		if err != nil {
			resp.Diagnostics.AddError("aws delete", err.Error())
		}
	case "azure":
		if r.azureLB == nil || r.azurePIP == nil {
			return
		}
		_, err := r.azureLB.BeginDelete(ctx, "abstract-rg", state.Name.ValueString(), nil)
		if err != nil {
			resp.Diagnostics.AddError("azure delete lb", err.Error())
		}
		pipName := state.Name.ValueString() + "-pip"
		_, err = r.azurePIP.BeginDelete(ctx, "abstract-rg", pipName, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure delete pip", err.Error())
		}
	}
}
