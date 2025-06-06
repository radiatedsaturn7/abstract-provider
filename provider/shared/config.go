package shared

import (
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"cloud.google.com/go/storage"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
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
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	cloudfunctions "google.golang.org/api/cloudfunctions/v1"
	compute "google.golang.org/api/compute/v1"
	container "google.golang.org/api/container/v1"
	dnsapi "google.golang.org/api/dns/v1"
	secretmanager "google.golang.org/api/secretmanager/v1"
	sqladmin "google.golang.org/api/sqladmin/v1beta4"
)

type ProviderConfig struct {
	AWSS3      *s3.Client
	AWSEC2     *ec2.Client
	AWSEKS     *eks.Client
	AWSLambda  *lambda.Client
	AWSRDS     *rds.Client
	AWSSQS     *sqs.Client
	AWSSM      *secretsmanager.Client
	AWSECR     *ecr.Client
	AWSECS     *ecs.Client
	AWSELB     *elbv2.Client
	AWSRoute53 *route53.Client

	AzureCred            azcore.TokenCredential
	AzureSubID           string
	AzureLocation        string
	AzureRGClient        *armresources.ResourceGroupsClient
	AzureStorageAcct     *armstorage.AccountsClient
	AzureBlobContainers  *armstorage.BlobContainersClient
	AzureVNetClient      *armnetwork.VirtualNetworksClient
	AzureSubnetClient    *armnetwork.SubnetsClient
	AzureNICClient       *armnetwork.InterfacesClient
	AzurePIPClient       *armnetwork.PublicIPAddressesClient
	AzureLBClient        *armnetwork.LoadBalancersClient
	AzureVMClient        *armcompute.VirtualMachinesClient
	AzureAKSClient       *armcontainerservice.ManagedClustersClient
	AzureWebClient       *armappservice.WebAppsClient
	AzurePlanClient      *armappservice.PlansClient
	AzureMySQLClient     *armmysqlflexibleservers.ServersClient
	AzurePostgresClient  *armpostgresqlflexibleservers.ServersClient
	AzureRegistryClient  *armcontainerregistry.RegistriesClient
	AzureContainerClient *ci.ContainerGroupsClient
	AzureDNSZoneClient   *armdns.ZonesClient
	AzureDNSRecordClient *armdns.RecordSetsClient

	GCPStorage   *storage.Client
	GCPCompute   *compute.Service
	GCPGKE       *container.Service
	GCPFunctions *cloudfunctions.Service
	GCPCloudSQL  *sqladmin.Service
	GCPDNS       *dnsapi.Service
	GCPSecrets   *secretmanager.Service
	GCPProject   string
	GCPRegion    string
}
