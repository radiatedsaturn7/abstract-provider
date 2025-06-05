package provider

import (
	"context"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"cloud.google.com/go/storage"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/mysql/armmysqlflexibleservers"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/postgresql/armpostgresqlflexibleservers"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
       compute "google.golang.org/api/compute/v1"
       container "google.golang.org/api/container/v1"
       cloudfunctions "google.golang.org/api/cloudfunctions/v1"
       sqladmin "google.golang.org/api/sqladmin/v1beta4"
       "google.golang.org/api/option"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	pschema "github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"

	"abstract-provider/provider/resources"
	"abstract-provider/provider/shared"
)

type abstractProvider struct {
	s3     *s3.Client
	ec2    *ec2.Client
	eks    *eks.Client
	lambda *lambda.Client
	rds    *rds.Client

	azureRG       *armresources.ResourceGroupsClient
	azureAcct     *armstorage.AccountsClient
	azureCont     *armstorage.BlobContainersClient
	azureVNet     *armnetwork.VirtualNetworksClient
	azureSubnets  *armnetwork.SubnetsClient
	azureNIC      *armnetwork.InterfacesClient
	azurePIP      *armnetwork.PublicIPAddressesClient
	azureVM       *armcompute.VirtualMachinesClient
	azureAKS      *armcontainerservice.ManagedClustersClient
	azureWeb      *armappservice.WebAppsClient
	azurePlan     *armappservice.PlansClient
	azureMySQL    *armmysqlflexibleservers.ServersClient
	azurePostgres *armpostgresqlflexibleservers.ServersClient
	azureSubID    string
	azureCred     *azidentity.ClientSecretCredential
	azureLoc      string

       gcpStorage *storage.Client
       gcpCompute *compute.Service
       gcpGKE     *container.Service
       gcpFunctions *cloudfunctions.Service
       gcpSQL     *sqladmin.Service
       gcpProject string
       gcpRegion  string
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
	baseCfg := &shared.ProviderConfig{AWSS3: p.s3, AWSEC2: p.ec2, AWSEKS: p.eks, AWSLambda: p.lambda, AWSRDS: p.rds}
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
		p.azureRG = rgClient
		p.azureAcct = acctClient
		p.azureCont = contClient
		p.azureVNet = vnetClient
		p.azureSubnets = subnetClient
		p.azureNIC = nicClient
		p.azurePIP = pipClient
		p.azureVM = vmClient
		p.azureAKS = aksClient
		p.azureWeb = webClient
		p.azurePlan = planClient
		p.azureMySQL = mysqlClient
		p.azurePostgres = pgClient
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
	baseCfg.AzureVMClient = p.azureVM
	baseCfg.AzureAKSClient = p.azureAKS
	baseCfg.AzureWebClient = p.azureWeb
	baseCfg.AzurePlanClient = p.azurePlan
	baseCfg.AzureMySQLClient = p.azureMySQL
	baseCfg.AzurePostgresClient = p.azurePostgres

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
               p.gcpProject = cfg.GCP.Project
               p.gcpRegion = cfg.GCP.Region
       }

	baseCfg.GCPStorage = p.gcpStorage
       baseCfg.GCPCompute = p.gcpCompute
       baseCfg.GCPGKE = p.gcpGKE
       baseCfg.GCPFunctions = p.gcpFunctions
       baseCfg.GCPCloudSQL = p.gcpSQL
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
	}
}

func (p *abstractProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return nil
}
