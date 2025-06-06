package provider

import (
	"context"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"cloud.google.com/go/storage"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	ci "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerinstance/armcontainerinstance"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/dns/armdns"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/mysql/armmysqlflexibleservers"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/postgresql/armpostgresqlflexibleservers"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	cloudfunctions "google.golang.org/api/cloudfunctions/v1"
	compute "google.golang.org/api/compute/v1"
	container "google.golang.org/api/container/v1"
	dnsapi "google.golang.org/api/dns/v1"
	"google.golang.org/api/option"
	secretmanager "google.golang.org/api/secretmanager/v1"
	sqladmin "google.golang.org/api/sqladmin/v1beta4"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	pschema "github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"

	"abstract-provider/provider/resources"
	"abstract-provider/provider/shared"
)

type abstractProvider struct {
	s3      *s3.Client
	ec2     *ec2.Client
	eks     *eks.Client
	lambda  *lambda.Client
	rds     *rds.Client
	sqs     *sqs.Client
	ecr     *ecr.Client
	ecs     *ecs.Client
	elb     *elasticloadbalancingv2.Client
	route53 *route53.Client
	secrets *secretsmanager.Client

	azureRG         *armresources.ResourceGroupsClient
	azureAcct       *armstorage.AccountsClient
	azureCont       *armstorage.BlobContainersClient
	azureVNet       *armnetwork.VirtualNetworksClient
	azureSubnets    *armnetwork.SubnetsClient
	azureNIC        *armnetwork.InterfacesClient
	azurePIP        *armnetwork.PublicIPAddressesClient
	azureLB         *armnetwork.LoadBalancersClient
	azureVM         *armcompute.VirtualMachinesClient
	azureAKS        *armcontainerservice.ManagedClustersClient
	azureWeb        *armappservice.WebAppsClient
	azurePlan       *armappservice.PlansClient
	azureMySQL      *armmysqlflexibleservers.ServersClient
	azurePostgres   *armpostgresqlflexibleservers.ServersClient
	azureRegistry   *armcontainerregistry.RegistriesClient
	azureCI         *ci.ContainerGroupsClient
	azureDNSZones   *armdns.ZonesClient
	azureDNSRecords *armdns.RecordSetsClient
	azureSubID      string
	azureCred       *azidentity.ClientSecretCredential
	azureLoc        string

	gcpStorage   *storage.Client
	gcpCompute   *compute.Service
	gcpGKE       *container.Service
	gcpFunctions *cloudfunctions.Service
	gcpSQL       *sqladmin.Service
	gcpDNS       *dnsapi.Service
	gcpSecrets   *secretmanager.Service
	gcpProject   string
	gcpRegion    string
}

func New() provider.Provider {
	return &abstractProvider{}
}

func (p *abstractProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "abstract"
}

func (p *abstractProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = pschema.Schema{
		Attributes: map[string]pschema.Attribute{
			"aws": pschema.SingleNestedAttribute{
				Optional: true,
				Attributes: map[string]pschema.Attribute{
					"region":     pschema.StringAttribute{Optional: true},
					"access_key": pschema.StringAttribute{Optional: true, Sensitive: true},
					"secret_key": pschema.StringAttribute{Optional: true, Sensitive: true},
				},
			},
			"azure": pschema.SingleNestedAttribute{
				Optional: true,
				Attributes: map[string]pschema.Attribute{
					"subscription_id": pschema.StringAttribute{Optional: true},
					"client_id":       pschema.StringAttribute{Optional: true, Sensitive: true},
					"client_secret":   pschema.StringAttribute{Optional: true, Sensitive: true},
					"tenant_id":       pschema.StringAttribute{Optional: true},
					"location":        pschema.StringAttribute{Optional: true},
				},
			},
			"gcp": pschema.SingleNestedAttribute{
				Optional: true,
				Attributes: map[string]pschema.Attribute{
					"project":     pschema.StringAttribute{Optional: true},
					"region":      pschema.StringAttribute{Optional: true},
					"credentials": pschema.StringAttribute{Optional: true, Sensitive: true},
				},
			},
		},
	}
}

func (p *abstractProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg struct {
		AWS struct {
			Region    string `tfsdk:"region"`
			AccessKey string `tfsdk:"access_key"`
			SecretKey string `tfsdk:"secret_key"`
		} `tfsdk:"aws"`
		Azure struct {
			SubscriptionID string `tfsdk:"subscription_id"`
			ClientID       string `tfsdk:"client_id"`
			ClientSecret   string `tfsdk:"client_secret"`
			TenantID       string `tfsdk:"tenant_id"`
			Location       string `tfsdk:"location"`
		} `tfsdk:"azure"`
		GCP struct {
			Project     string `tfsdk:"project"`
			Region      string `tfsdk:"region"`
			Credentials string `tfsdk:"credentials"`
		} `tfsdk:"gcp"`
	}

	diags := req.Config.Get(ctx, &cfg)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		resp.Diagnostics.AddError("aws config", err.Error())
		return
	}
	if cfg.AWS.Region != "" {
		awsCfg.Region = cfg.AWS.Region
	}
	if cfg.AWS.AccessKey != "" && cfg.AWS.SecretKey != "" {
		awsCfg.Credentials = credentials.NewStaticCredentialsProvider(cfg.AWS.AccessKey, cfg.AWS.SecretKey, "")
	}

	p.s3 = s3.NewFromConfig(awsCfg)
	p.ec2 = ec2.NewFromConfig(awsCfg)
	p.eks = eks.NewFromConfig(awsCfg)
	p.lambda = lambda.NewFromConfig(awsCfg)
	p.rds = rds.NewFromConfig(awsCfg)
	p.sqs = sqs.NewFromConfig(awsCfg)
	p.ecr = ecr.NewFromConfig(awsCfg)
	p.ecs = ecs.NewFromConfig(awsCfg)
	p.elb = elasticloadbalancingv2.NewFromConfig(awsCfg)
	p.route53 = route53.NewFromConfig(awsCfg)
	p.secrets = secretsmanager.NewFromConfig(awsCfg)
	baseCfg := &shared.ProviderConfig{AWSS3: p.s3, AWSEC2: p.ec2, AWSEKS: p.eks, AWSLambda: p.lambda, AWSRDS: p.rds, AWSSQS: p.sqs, AWSECR: p.ecr, AWSECS: p.ecs, AWSELB: p.elb, AWSRoute53: p.route53, AWSSM: p.secrets}
	resp.DataSourceData = baseCfg
	// base config before cloud-specific additions

	// Azure setup
	if cfg.Azure.SubscriptionID != "" && cfg.Azure.ClientID != "" && cfg.Azure.ClientSecret != "" && cfg.Azure.TenantID != "" {
		cred, err := azidentity.NewClientSecretCredential(cfg.Azure.TenantID, cfg.Azure.ClientID, cfg.Azure.ClientSecret, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure credential", err.Error())
			return
		}
		rgClient, err := armresources.NewResourceGroupsClient(cfg.Azure.SubscriptionID, cred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure rg client", err.Error())
			return
		}
		acctClient, err := armstorage.NewAccountsClient(cfg.Azure.SubscriptionID, cred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure account client", err.Error())
			return
		}
		contClient, err := armstorage.NewBlobContainersClient(cfg.Azure.SubscriptionID, cred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure container client", err.Error())
			return
		}
		vnetClient, err := armnetwork.NewVirtualNetworksClient(cfg.Azure.SubscriptionID, cred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure vnet client", err.Error())
			return
		}
		subnetClient, err := armnetwork.NewSubnetsClient(cfg.Azure.SubscriptionID, cred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure subnet client", err.Error())
			return
		}
		nicClient, err := armnetwork.NewInterfacesClient(cfg.Azure.SubscriptionID, cred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure nic client", err.Error())
			return
		}
		pipClient, err := armnetwork.NewPublicIPAddressesClient(cfg.Azure.SubscriptionID, cred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure pip client", err.Error())
			return
		}
		lbClient, err := armnetwork.NewLoadBalancersClient(cfg.Azure.SubscriptionID, cred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure lb client", err.Error())
			return
		}
		vmClient, err := armcompute.NewVirtualMachinesClient(cfg.Azure.SubscriptionID, cred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure vm client", err.Error())
			return
		}
		aksClient, err := armcontainerservice.NewManagedClustersClient(cfg.Azure.SubscriptionID, cred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure aks client", err.Error())
			return
		}
		webClient, err := armappservice.NewWebAppsClient(cfg.Azure.SubscriptionID, cred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure web client", err.Error())
			return
		}
		planClient, err := armappservice.NewPlansClient(cfg.Azure.SubscriptionID, cred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure plan client", err.Error())
			return
		}
		mysqlClient, err := armmysqlflexibleservers.NewServersClient(cfg.Azure.SubscriptionID, cred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure mysql client", err.Error())
			return
		}
		pgClient, err := armpostgresqlflexibleservers.NewServersClient(cfg.Azure.SubscriptionID, cred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure postgres client", err.Error())
			return
		}
		regClient, err := armcontainerregistry.NewRegistriesClient(cfg.Azure.SubscriptionID, cred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure registry client", err.Error())
			return
		}
		ciClient, err := ci.NewContainerGroupsClient(cfg.Azure.SubscriptionID, cred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure container client", err.Error())
			return
		}
		dnsZoneClient, err := armdns.NewZonesClient(cfg.Azure.SubscriptionID, cred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure dns zone client", err.Error())
			return
		}
		dnsRecordClient, err := armdns.NewRecordSetsClient(cfg.Azure.SubscriptionID, cred, nil)
		if err != nil {
			resp.Diagnostics.AddError("azure dns record client", err.Error())
			return
		}
		p.azureRG = rgClient
		p.azureAcct = acctClient
		p.azureCont = contClient
		p.azureVNet = vnetClient
		p.azureSubnets = subnetClient
		p.azureNIC = nicClient
		p.azurePIP = pipClient
		p.azureLB = lbClient
		p.azureVM = vmClient
		p.azureAKS = aksClient
		p.azureWeb = webClient
		p.azurePlan = planClient
		p.azureMySQL = mysqlClient
		p.azurePostgres = pgClient
		p.azureRegistry = regClient
		p.azureCI = ciClient
		p.azureDNSZones = dnsZoneClient
		p.azureDNSRecords = dnsRecordClient
		p.azureSubID = cfg.Azure.SubscriptionID
		p.azureCred = cred
		p.azureLoc = cfg.Azure.Location
	}

	baseCfg.AzureCred = p.azureCred
	baseCfg.AzureSubID = p.azureSubID
	baseCfg.AzureLocation = p.azureLoc
	baseCfg.AzureRGClient = p.azureRG
	baseCfg.AzureStorageAcct = p.azureAcct
	baseCfg.AzureBlobContainers = p.azureCont
	baseCfg.AzureVNetClient = p.azureVNet
	baseCfg.AzureSubnetClient = p.azureSubnets
	baseCfg.AzureNICClient = p.azureNIC
	baseCfg.AzurePIPClient = p.azurePIP
	baseCfg.AzureLBClient = p.azureLB
	baseCfg.AzureVMClient = p.azureVM
	baseCfg.AzureAKSClient = p.azureAKS
	baseCfg.AzureWebClient = p.azureWeb
	baseCfg.AzurePlanClient = p.azurePlan
	baseCfg.AzureMySQLClient = p.azureMySQL
	baseCfg.AzurePostgresClient = p.azurePostgres
	baseCfg.AzureRegistryClient = p.azureRegistry
	baseCfg.AzureContainerClient = p.azureCI
	baseCfg.AzureDNSZoneClient = p.azureDNSZones
	baseCfg.AzureDNSRecordClient = p.azureDNSRecords

	// GCP setup
	if cfg.GCP.Project != "" {
		var opts []option.ClientOption
		if cfg.GCP.Credentials != "" {
			opts = append(opts, option.WithCredentialsJSON([]byte(cfg.GCP.Credentials)))
		}
		storageClient, err := storage.NewClient(ctx, opts...)
		if err != nil {
			resp.Diagnostics.AddError("gcp storage client", err.Error())
			return
		}
		computeSvc, err := compute.NewService(ctx, opts...)
		if err != nil {
			resp.Diagnostics.AddError("gcp compute client", err.Error())
			return
		}
		gkeSvc, err := container.NewService(ctx, opts...)
		if err != nil {
			resp.Diagnostics.AddError("gcp gke client", err.Error())
			return
		}
		funcSvc, err := cloudfunctions.NewService(ctx, opts...)
		if err != nil {
			resp.Diagnostics.AddError("gcp functions client", err.Error())
			return
		}
		secretSvc, err := secretmanager.NewService(ctx, opts...)
		if err != nil {
			resp.Diagnostics.AddError("gcp secret client", err.Error())
			return
		}
		dnsSvc, err := dnsapi.NewService(ctx, opts...)
		if err != nil {
			resp.Diagnostics.AddError("gcp dns client", err.Error())
			return
		}
		sqlSvc, err := sqladmin.NewService(ctx, opts...)
		if err != nil {
			resp.Diagnostics.AddError("gcp sql client", err.Error())
			return
		}
		p.gcpStorage = storageClient
		p.gcpCompute = computeSvc
		p.gcpGKE = gkeSvc
		p.gcpFunctions = funcSvc
		p.gcpSQL = sqlSvc
		p.gcpSecrets = secretSvc
		p.gcpDNS = dnsSvc
		p.gcpProject = cfg.GCP.Project
		p.gcpRegion = cfg.GCP.Region
	}

	baseCfg.GCPStorage = p.gcpStorage
	baseCfg.GCPCompute = p.gcpCompute
	baseCfg.GCPGKE = p.gcpGKE
	baseCfg.GCPFunctions = p.gcpFunctions
	baseCfg.GCPCloudSQL = p.gcpSQL
	baseCfg.GCPDNS = p.gcpDNS
	baseCfg.GCPSecrets = p.gcpSecrets
	baseCfg.GCPProject = p.gcpProject
	baseCfg.GCPRegion = p.gcpRegion
	resp.ResourceData = baseCfg
}

func (p *abstractProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		resources.NewBucketResource,
		resources.NewNetworkResource,
		resources.NewInstanceResource,
		resources.NewClusterResource,
		resources.NewFunctionResource,
		resources.NewDatabaseResource,
		resources.NewQueueResource,
		resources.NewRegistryResource,
		resources.NewLoadBalancerResource,
		resources.NewServerlessContainerResource,
		resources.NewDNSRecordResource,
		resources.NewSecretResource,
	}
}

func (p *abstractProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return nil
}
