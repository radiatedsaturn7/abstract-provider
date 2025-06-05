package resources

import (
	"context"
	"fmt"
	"strings"

	"abstract-provider/provider/shared"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	schema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	compute "google.golang.org/api/compute/v1"
)

type InstanceResource struct {
	ec2 *ec2.Client

	azureVM   *armcompute.VirtualMachinesClient
	azureNIC  *armnetwork.InterfacesClient
	azurePIP  *armnetwork.PublicIPAddressesClient
	azureRG   *armresources.ResourceGroupsClient
	azureVNet *armnetwork.VirtualNetworksClient
	azureSub  *armnetwork.SubnetsClient
	azureCred azcore.TokenCredential
	azureLoc  string

	gcp       *compute.Service
	gcpProj   string
	gcpRegion string
}

func NewInstanceResource() resource.Resource { return &InstanceResource{} }

func (r *InstanceResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	cfg, ok := req.ProviderData.(*shared.ProviderConfig)
	if !ok {
		resp.Diagnostics.AddError("invalid provider data", "")
		return
	}
	r.ec2 = cfg.AWSEC2
	r.azureVM = cfg.AzureVMClient
	r.azureNIC = cfg.AzureNICClient
	r.azurePIP = cfg.AzurePIPClient
	r.azureRG = cfg.AzureRGClient
	r.azureVNet = cfg.AzureVNetClient
	r.azureSub = cfg.AzureSubnetClient
	r.azureCred = cfg.AzureCred
	r.azureLoc = cfg.AzureLocation
	r.gcp = cfg.GCPCompute
	r.gcpProj = cfg.GCPProject
	r.gcpRegion = cfg.GCPRegion
}

func (r *InstanceResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "abstract_instance"
}

func (r *InstanceResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id":        schema.StringAttribute{Computed: true},
			"name":      schema.StringAttribute{Optional: true},
			"type":      schema.StringAttribute{Required: true},
			"region":    schema.StringAttribute{Optional: true},
			"image":     schema.StringAttribute{Optional: true},
			"size":      schema.StringAttribute{Optional: true},
			"public_ip": schema.BoolAttribute{Optional: true},
		},
	}
}

func (r *InstanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan struct {
		Name     types.String `tfsdk:"name"`
		Type     types.String `tfsdk:"type"`
		Region   types.String `tfsdk:"region"`
		Image    types.String `tfsdk:"image"`
		Size     types.String `tfsdk:"size"`
		PublicIP types.Bool   `tfsdk:"public_ip"`
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
		if plan.Image.ValueString() == "" {
			resp.Diagnostics.AddError("missing image", "ami id must be provided")
			return
		}
		size := plan.Size.ValueString()
		if size == "" {
			size = "small"
		}
		instanceType := size
		switch strings.ToLower(size) {
		case "small":
			instanceType = string(ec2types.InstanceTypeT3Small)
		case "medium":
			instanceType = string(ec2types.InstanceTypeT3Medium)
		case "large":
			instanceType = string(ec2types.InstanceTypeT3Large)
		default:
			instanceType = size
		}
		input := &ec2.RunInstancesInput{
			ImageId:      aws.String(plan.Image.ValueString()),
			InstanceType: ec2types.InstanceType(instanceType),
			MinCount:     aws.Int32(1),
			MaxCount:     aws.Int32(1),
		}
		if plan.PublicIP.ValueBool() {
			input.NetworkInterfaces = []ec2types.InstanceNetworkInterfaceSpecification{{
				DeviceIndex:              aws.Int32(0),
				AssociatePublicIpAddress: aws.Bool(true),
			}}
		}
		out, err := r.ec2.RunInstances(ctx, input)
		if err != nil || len(out.Instances) == 0 {
			if err == nil {
				err = fmt.Errorf("no instance returned")
			}
			resp.Diagnostics.AddError("aws run instance", err.Error())
			return
		}
		id := aws.ToString(out.Instances[0].InstanceId)
		if plan.Name.ValueString() != "" {
			_, err = r.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
				Resources: []string{id},
				Tags:      []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String(plan.Name.ValueString())}},
			})
			if err != nil {
				resp.Diagnostics.AddError("aws tag instance", err.Error())
				return
			}
		}
		resp.State.Set(ctx, map[string]interface{}{
			"id":        id,
			"name":      plan.Name.ValueString(),
			"type":      plan.Type.ValueString(),
			"region":    plan.Region.ValueString(),
			"image":     plan.Image.ValueString(),
			"size":      instanceType,
			"public_ip": plan.PublicIP.ValueBool(),
		})
	case "azure":
		if r.azureVM == nil || r.azureNIC == nil || r.azurePIP == nil || r.azureRG == nil || r.azureSub == nil {
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
		vnetName := "abstract-vnet"
		subnetName := "default"
		// ensure subnet exists
		subnetResp, err := r.azureSub.Get(ctx, rgName, vnetName, subnetName, nil)
		if err != nil || subnetResp.ID == nil {
			// create vnet and subnet if not existing
			vnetPoller, verr := r.azureVNet.BeginCreateOrUpdate(ctx, rgName, vnetName, armnetwork.VirtualNetwork{
				Location: &r.azureLoc,
				Properties: &armnetwork.VirtualNetworkPropertiesFormat{
					AddressSpace: &armnetwork.AddressSpace{AddressPrefixes: []*string{to.Ptr("10.0.0.0/16")}},
				},
			}, nil)
			if verr == nil {
				_, verr = vnetPoller.PollUntilDone(ctx, nil)
			}
			if verr != nil {
				resp.Diagnostics.AddError("azure vnet", verr.Error())
				return
			}
			subnetPoller, serr := r.azureSub.BeginCreateOrUpdate(ctx, rgName, vnetName, subnetName, armnetwork.Subnet{
				Properties: &armnetwork.SubnetPropertiesFormat{AddressPrefix: to.Ptr("10.0.0.0/24")},
			}, nil)
			if serr == nil {
				subResp, serr := subnetPoller.PollUntilDone(ctx, nil)
				if serr == nil {
					subnetResp.Subnet = subResp.Subnet
				}
				err = serr
			} else {
				err = serr
			}
			if err != nil {
				resp.Diagnostics.AddError("azure subnet", err.Error())
				return
			}
		}
		subnetID := *subnetResp.ID
		pipName := plan.Name.ValueString() + "-pip"
		pipPoller, err := r.azurePIP.BeginCreateOrUpdate(ctx, rgName, pipName, armnetwork.PublicIPAddress{
			Location: &r.azureLoc,
			Properties: &armnetwork.PublicIPAddressPropertiesFormat{
				PublicIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodDynamic),
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
		nicName := plan.Name.ValueString() + "-nic"
		nicPoller, err := r.azureNIC.BeginCreateOrUpdate(ctx, rgName, nicName, armnetwork.Interface{
			Location: &r.azureLoc,
			Properties: &armnetwork.InterfacePropertiesFormat{
				IPConfigurations: []*armnetwork.InterfaceIPConfiguration{{
					Name: to.Ptr("ipconfig1"),
					Properties: &armnetwork.InterfaceIPConfigurationPropertiesFormat{
						Subnet:          &armnetwork.Subnet{ID: &subnetID},
						PublicIPAddress: &armnetwork.PublicIPAddress{ID: &pipID},
					},
				}},
			},
		}, nil)
		var nicID string
		if err == nil {
			nicResp, nerr := nicPoller.PollUntilDone(ctx, nil)
			err = nerr
			if nerr == nil && nicResp.ID != nil {
				nicID = *nicResp.ID
			}
		}
		if err != nil {
			resp.Diagnostics.AddError("azure nic", err.Error())
			return
		}

		size := plan.Size.ValueString()
		if size == "" {
			size = "small"
		}
		vmSize := size
		switch strings.ToLower(size) {
		case "small":
			vmSize = string(armcompute.VirtualMachineSizeTypesStandardB1S)
		case "medium":
			vmSize = string(armcompute.VirtualMachineSizeTypesStandardB2S)
		case "large":
			vmSize = string(armcompute.VirtualMachineSizeTypesStandardB4Ms)
		default:
			vmSize = size
		}
		imageRef := &armcompute.ImageReference{
			Publisher: to.Ptr("Canonical"),
			Offer:     to.Ptr("0001-com-ubuntu-server-jammy"),
			SKU:       to.Ptr("22_04-lts"),
			Version:   to.Ptr("latest"),
		}
		vmPoller, err := r.azureVM.BeginCreateOrUpdate(ctx, rgName, plan.Name.ValueString(), armcompute.VirtualMachine{
			Location: &r.azureLoc,
			Properties: &armcompute.VirtualMachineProperties{
				HardwareProfile: &armcompute.HardwareProfile{VMSize: to.Ptr(armcompute.VirtualMachineSizeTypes(vmSize))},
				StorageProfile: &armcompute.StorageProfile{
					ImageReference: imageRef,
					OSDisk: &armcompute.OSDisk{
						CreateOption: to.Ptr(armcompute.DiskCreateOptionTypesFromImage),
						ManagedDisk:  &armcompute.ManagedDiskParameters{StorageAccountType: to.Ptr(armcompute.StorageAccountTypesStandardLRS)},
					},
				},
				OSProfile: &armcompute.OSProfile{
					ComputerName:  to.Ptr(plan.Name.ValueString()),
					AdminUsername: to.Ptr("azureuser"),
					AdminPassword: to.Ptr("Password1234!"),
				},
				NetworkProfile: &armcompute.NetworkProfile{
					NetworkInterfaces: []*armcompute.NetworkInterfaceReference{{
						ID:         &nicID,
						Properties: &armcompute.NetworkInterfaceReferenceProperties{Primary: to.Ptr(true)},
					}},
				},
			},
		}, nil)
		var vmID string
		if err == nil {
			vmResp, verr := vmPoller.PollUntilDone(ctx, nil)
			err = verr
			if verr == nil && vmResp.ID != nil {
				vmID = *vmResp.ID
			}
		}
		if err != nil {
			resp.Diagnostics.AddError("azure vm", err.Error())
			return
		}
		resp.State.Set(ctx, map[string]interface{}{
			"id":        vmID,
			"name":      plan.Name.ValueString(),
			"type":      plan.Type.ValueString(),
			"region":    r.azureLoc,
			"image":     plan.Image.ValueString(),
			"size":      vmSize,
			"public_ip": plan.PublicIP.ValueBool(),
		})
	case "gcp":
		if r.gcp == nil {
			resp.Diagnostics.AddError("gcp", "missing client")
			return
		}
		zone := plan.Region.ValueString()
		if zone == "" {
			zone = r.gcpRegion
		}
		if zone == "" {
			zone = "us-central1-a"
		}
		size := plan.Size.ValueString()
		if size == "" {
			size = "small"
		}
		machineType := size
		switch strings.ToLower(size) {
		case "small":
			machineType = "e2-small"
		case "medium":
			machineType = "e2-medium"
		case "large":
			machineType = "e2-standard-4"
		default:
			machineType = size
		}
		image := plan.Image.ValueString()
		if image == "" {
			image = "projects/debian-cloud/global/images/family/debian-11"
		}
		inst := &compute.Instance{
			Name:        plan.Name.ValueString(),
			MachineType: fmt.Sprintf("zones/%s/machineTypes/%s", zone, machineType),
			Disks: []*compute.AttachedDisk{{
				Boot:             true,
				AutoDelete:       true,
				InitializeParams: &compute.AttachedDiskInitializeParams{SourceImage: image},
			}},
			NetworkInterfaces: []*compute.NetworkInterface{{
				Network: fmt.Sprintf("projects/%s/global/networks/default", r.gcpProj),
			}},
		}
		if plan.PublicIP.ValueBool() {
			inst.NetworkInterfaces[0].AccessConfigs = []*compute.AccessConfig{{
				Name: "External",
				Type: "ONE_TO_ONE_NAT",
			}}
		}
		_, err := r.gcp.Instances.Insert(r.gcpProj, zone, inst).Context(ctx).Do()
		if err != nil {
			resp.Diagnostics.AddError("gcp create instance", err.Error())
			return
		}
		resp.State.Set(ctx, map[string]interface{}{
			"id":        inst.Name,
			"name":      plan.Name.ValueString(),
			"type":      plan.Type.ValueString(),
			"region":    zone,
			"image":     image,
			"size":      machineType,
			"public_ip": plan.PublicIP.ValueBool(),
		})
	default:
		resp.Diagnostics.AddError("unsupported cloud", "only aws and azure implemented")
	}
}

func (r *InstanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state struct {
		ID     types.String `tfsdk:"id"`
		Type   types.String `tfsdk:"type"`
		Region types.String `tfsdk:"region"`
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
		out, err := r.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{state.ID.ValueString()}})
		if err != nil || len(out.Reservations) == 0 || len(out.Reservations[0].Instances) == 0 {
			resp.State.RemoveResource(ctx)
		}
	case "azure":
		if r.azureVM == nil {
			return
		}
		_, err := r.azureVM.Get(ctx, "abstract-rg", state.ID.ValueString(), nil)
		if err != nil {
			resp.State.RemoveResource(ctx)
		}
	case "gcp":
		if r.gcp == nil {
			return
		}
		zone := state.Region.ValueString()
		if zone == "" {
			zone = r.gcpRegion
		}
		if zone == "" {
			zone = "us-central1-a"
		}
		_, err := r.gcp.Instances.Get(r.gcpProj, zone, state.ID.ValueString()).Context(ctx).Do()
		if err != nil {
			resp.State.RemoveResource(ctx)
		}
	}
}
func (r *InstanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
}
func (r *InstanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state struct {
		ID     types.String `tfsdk:"id"`
		Type   types.String `tfsdk:"type"`
		Region types.String `tfsdk:"region"`
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
		_, err := r.ec2.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{state.ID.ValueString()}})
		if err != nil {
			resp.Diagnostics.AddError("aws terminate", err.Error())
		}
	case "azure":
		if r.azureVM == nil {
			return
		}
		poller, err := r.azureVM.BeginDelete(ctx, "abstract-rg", state.ID.ValueString(), nil)
		if err == nil {
			_, err = poller.PollUntilDone(ctx, nil)
		}
		if err != nil {
			resp.Diagnostics.AddError("azure delete", err.Error())
		}
	case "gcp":
		if r.gcp == nil {
			return
		}
		zone := state.Region.ValueString()
		if zone == "" {
			zone = r.gcpRegion
		}
		if zone == "" {
			zone = "us-central1-a"
		}
		_, err := r.gcp.Instances.Delete(r.gcpProj, zone, state.ID.ValueString()).Context(ctx).Do()
		if err != nil {
			resp.Diagnostics.AddError("gcp delete", err.Error())
		}
	}
}
