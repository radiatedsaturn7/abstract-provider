package resources

import (
	"context"
	"fmt"
	"strings"

	"abstract-provider/provider/shared"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/dns/armdns"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	schema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	dnsapi "google.golang.org/api/dns/v1"
)

// DNSRecordResource implements cross-cloud DNS records.
type DNSRecordResource struct {
	route53      *route53.Client
	azureRG      *armresources.ResourceGroupsClient
	azureZones   *armdns.ZonesClient
	azureRecords *armdns.RecordSetsClient
	azureCred    azcore.TokenCredential
	azureSub     string
	gcpDNS       *dnsapi.Service
	gcpProject   string
}

func NewDNSRecordResource() resource.Resource { return &DNSRecordResource{} }

func (r *DNSRecordResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	cfg, ok := req.ProviderData.(*shared.ProviderConfig)
	if !ok {
		resp.Diagnostics.AddError("invalid provider data", "")
		return
	}
	r.route53 = cfg.AWSRoute53
	r.azureRG = cfg.AzureRGClient
	r.azureZones = cfg.AzureDNSZoneClient
	r.azureRecords = cfg.AzureDNSRecordClient
	r.azureCred = cfg.AzureCred
	r.azureSub = cfg.AzureSubID
	r.gcpDNS = cfg.GCPDNS
	r.gcpProject = cfg.GCPProject
}

func (r *DNSRecordResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "abstract_dns_record"
}

func (r *DNSRecordResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id":    schema.StringAttribute{Computed: true},
			"name":  schema.StringAttribute{Required: true},
			"zone":  schema.StringAttribute{Required: true},
			"type":  schema.StringAttribute{Required: true},
			"value": schema.StringAttribute{Required: true},
			"ttl":   schema.Int64Attribute{Optional: true, Computed: true},
		},
	}
}

func (r *DNSRecordResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan struct {
		Name  types.String `tfsdk:"name"`
		Zone  types.String `tfsdk:"zone"`
		Type  types.String `tfsdk:"type"`
		Value types.String `tfsdk:"value"`
		TTL   types.Int64  `tfsdk:"ttl"`
	}
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ttl := int64(300)
	if !plan.TTL.IsNull() {
		ttl = plan.TTL.ValueInt64()
	}
	fqdn := plan.Name.ValueString()
	if !strings.HasSuffix(fqdn, plan.Zone.ValueString()+".") {
		fqdn = fqdn + "." + plan.Zone.ValueString() + "."
	}
	switch strings.ToLower(plan.Type.ValueString()) {
	case "aws":
		if r.route53 == nil {
			resp.Diagnostics.AddError("aws", "missing client")
			return
		}
		// lookup zone
		out, err := r.route53.ListHostedZonesByName(ctx, &route53.ListHostedZonesByNameInput{DNSName: aws.String(plan.Zone.ValueString())})
		if err != nil || len(out.HostedZones) == 0 {
			resp.Diagnostics.AddError("aws zone", "not found")
			return
		}
		zoneID := aws.ToString(out.HostedZones[0].Id)
		_, err = r.route53.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
			HostedZoneId: aws.String(zoneID),
			ChangeBatch: &r53types.ChangeBatch{Changes: []r53types.Change{{
				Action: r53types.ChangeActionUpsert,
				ResourceRecordSet: &r53types.ResourceRecordSet{
					Name:            aws.String(fqdn),
					Type:            r53types.RRType(plan.Type.ValueString()),
					TTL:             aws.Int64(ttl),
					ResourceRecords: []r53types.ResourceRecord{{Value: aws.String(plan.Value.ValueString())}},
				},
			}}},
		})
		if err != nil {
			resp.Diagnostics.AddError("aws create", err.Error())
			return
		}
		resp.State.Set(ctx, map[string]interface{}{
			"id":    fmt.Sprintf("%s/%s", zoneID, fqdn),
			"name":  plan.Name.ValueString(),
			"zone":  plan.Zone.ValueString(),
			"type":  plan.Type.ValueString(),
			"value": plan.Value.ValueString(),
			"ttl":   ttl,
		})
	case "azure":
		if r.azureZones == nil || r.azureRecords == nil || r.azureRG == nil {
			resp.Diagnostics.AddError("azure", "missing client")
			return
		}
		rg := "abstract-dns-rg"
		_, err := r.azureRG.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: to.Ptr("global")}, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure rg", err.Error())
			return
		}
		_, err = r.azureZones.CreateOrUpdate(ctx, rg, plan.Zone.ValueString(), armdns.Zone{Location: to.Ptr("global")}, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure zone", err.Error())
			return
		}
		recordType := armdns.RecordTypeA
		if strings.EqualFold(plan.Type.ValueString(), "CNAME") {
			recordType = armdns.RecordTypeCNAME
		}
		setParams := armdns.RecordSet{Properties: &armdns.RecordSetProperties{TTL: to.Ptr(ttl)}}
		if recordType == armdns.RecordTypeA {
			setParams.Properties.ARecords = []*armdns.ARecord{{IPv4Address: to.Ptr(plan.Value.ValueString())}}
		} else {
			setParams.Properties.CnameRecord = &armdns.CnameRecord{Cname: to.Ptr(plan.Value.ValueString())}
		}
		_, err = r.azureRecords.CreateOrUpdate(ctx, rg, plan.Zone.ValueString(), fqdn, recordType, setParams, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure record", err.Error())
			return
		}
		resp.State.Set(ctx, map[string]interface{}{
			"id":             fmt.Sprintf("%s/%s", plan.Zone.ValueString(), fqdn),
			"name":           plan.Name.ValueString(),
			"zone":           plan.Zone.ValueString(),
			"type":           plan.Type.ValueString(),
			"value":          plan.Value.ValueString(),
			"ttl":            ttl,
			"resource_group": rg,
		})
	case "gcp":
		if r.gcpDNS == nil {
			resp.Diagnostics.AddError("gcp", "missing client")
			return
		}
		// ensure zone exists
		_, err := r.gcpDNS.ManagedZones.Get(r.gcpProject, plan.Zone.ValueString()).Context(ctx).Do()
		if err != nil {
			zone := &dnsapi.ManagedZone{Name: plan.Zone.ValueString(), DnsName: plan.Zone.ValueString() + "."}
			_, err = r.gcpDNS.ManagedZones.Create(r.gcpProject, zone).Context(ctx).Do()
			if err != nil {
				resp.Diagnostics.AddError("gcp zone", err.Error())
				return
			}
		}
		change := &dnsapi.Change{Additions: []*dnsapi.ResourceRecordSet{{Name: fqdn, Type: strings.ToUpper(plan.Type.ValueString()), Ttl: ttl, Rrdatas: []string{plan.Value.ValueString()}}}}
		_, err = r.gcpDNS.Changes.Create(r.gcpProject, plan.Zone.ValueString(), change).Context(ctx).Do()
		if err != nil {
			resp.Diagnostics.AddError("gcp record", err.Error())
			return
		}
		resp.State.Set(ctx, map[string]interface{}{
			"id":    fmt.Sprintf("%s/%s", plan.Zone.ValueString(), fqdn),
			"name":  plan.Name.ValueString(),
			"zone":  plan.Zone.ValueString(),
			"type":  plan.Type.ValueString(),
			"value": plan.Value.ValueString(),
			"ttl":   ttl,
		})
	default:
		resp.Diagnostics.AddError("unsupported cloud", "")
	}
}

func (r *DNSRecordResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state struct {
		ID            types.String `tfsdk:"id"`
		Zone          types.String `tfsdk:"zone"`
		Name          types.String `tfsdk:"name"`
		Type          types.String `tfsdk:"type"`
		Value         types.String `tfsdk:"value"`
		TTL           types.Int64  `tfsdk:"ttl"`
		ResourceGroup types.String `tfsdk:"resource_group"`
	}
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	fqdn := state.Name.ValueString()
	if !strings.HasSuffix(fqdn, state.Zone.ValueString()+".") {
		fqdn = fqdn + "." + state.Zone.ValueString() + "."
	}
	switch strings.ToLower(state.Type.ValueString()) {
	case "aws":
		if r.route53 == nil {
			return
		}
		out, err := r.route53.ListHostedZonesByName(ctx, &route53.ListHostedZonesByNameInput{DNSName: aws.String(state.Zone.ValueString())})
		if err != nil || len(out.HostedZones) == 0 {
			resp.State.RemoveResource(ctx)
			return
		}
		zoneID := aws.ToString(out.HostedZones[0].Id)
		rsOut, err := r.route53.ListResourceRecordSets(ctx, &route53.ListResourceRecordSetsInput{HostedZoneId: aws.String(zoneID), StartRecordName: aws.String(fqdn), StartRecordType: r53types.RRType(strings.ToUpper(state.Type.ValueString()))})
		if err != nil || len(rsOut.ResourceRecordSets) == 0 {
			resp.State.RemoveResource(ctx)
			return
		}
	case "azure":
		if r.azureRecords == nil {
			return
		}
		rg := state.ResourceGroup.ValueString()
		if rg == "" {
			rg = "abstract-dns-rg"
		}
		_, err := r.azureRecords.Get(ctx, rg, state.Zone.ValueString(), fqdn, armdns.RecordType(strings.ToUpper(state.Type.ValueString())), nil)
		if err != nil {
			resp.State.RemoveResource(ctx)
			return
		}
	case "gcp":
		if r.gcpDNS == nil {
			return
		}
		rsOut, err := r.gcpDNS.ResourceRecordSets.List(r.gcpProject, state.Zone.ValueString()).Name(fqdn).Type(strings.ToUpper(state.Type.ValueString())).Context(ctx).Do()
		if err != nil || len(rsOut.Rrsets) == 0 {
			resp.State.RemoveResource(ctx)
			return
		}
	}
}

func (r *DNSRecordResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// simplified: delete then create
	var plan struct {
		Name  types.String `tfsdk:"name"`
		Zone  types.String `tfsdk:"zone"`
		Type  types.String `tfsdk:"type"`
		Value types.String `tfsdk:"value"`
		TTL   types.Int64  `tfsdk:"ttl"`
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

func (r *DNSRecordResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state struct {
		Zone          types.String `tfsdk:"zone"`
		Name          types.String `tfsdk:"name"`
		Type          types.String `tfsdk:"type"`
		ResourceGroup types.String `tfsdk:"resource_group"`
	}
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	fqdn := state.Name.ValueString()
	if !strings.HasSuffix(fqdn, state.Zone.ValueString()+".") {
		fqdn = fqdn + "." + state.Zone.ValueString() + "."
	}
	switch strings.ToLower(state.Type.ValueString()) {
	case "aws":
		if r.route53 == nil {
			return
		}
		out, err := r.route53.ListHostedZonesByName(ctx, &route53.ListHostedZonesByNameInput{DNSName: aws.String(state.Zone.ValueString())})
		if err != nil || len(out.HostedZones) == 0 {
			return
		}
		zoneID := aws.ToString(out.HostedZones[0].Id)
		_, err = r.route53.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
			HostedZoneId: aws.String(zoneID),
			ChangeBatch: &r53types.ChangeBatch{Changes: []r53types.Change{{
				Action: r53types.ChangeActionDelete,
				ResourceRecordSet: &r53types.ResourceRecordSet{
					Name:            aws.String(fqdn),
					Type:            r53types.RRType(strings.ToUpper(state.Type.ValueString())),
					TTL:             aws.Int64(300),
					ResourceRecords: []r53types.ResourceRecord{{Value: aws.String("")}},
				},
			}}},
		})
		_ = err
	case "azure":
		if r.azureRecords == nil {
			return
		}
		rg := state.ResourceGroup.ValueString()
		if rg == "" {
			rg = "abstract-dns-rg"
		}
		_, _ = r.azureRecords.Delete(ctx, rg, state.Zone.ValueString(), fqdn, armdns.RecordType(strings.ToUpper(state.Type.ValueString())), nil)
	case "gcp":
		if r.gcpDNS == nil {
			return
		}
		change := &dnsapi.Change{Deletions: []*dnsapi.ResourceRecordSet{{Name: fqdn, Type: strings.ToUpper(state.Type.ValueString()), Ttl: 300, Rrdatas: []string{}}}}
		_, _ = r.gcpDNS.Changes.Create(r.gcpProject, state.Zone.ValueString(), change).Context(ctx).Do()
	}
}
