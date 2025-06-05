package shared

import (
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"cloud.google.com/go/storage"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/mysql/armmysqlflexibleservers"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/postgresql/armpostgresqlflexibleservers"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	compute "google.golang.org/api/compute/v1"
       container "google.golang.org/api/container/v1"
       cloudfunctions "google.golang.org/api/cloudfunctions/v1"
       sqladmin "google.golang.org/api/sqladmin/v1beta4"
)

type ProviderConfig struct {
	AWSS3     *s3.Client
	AWSEC2    *ec2.Client
	AWSEKS    *eks.Client
	AWSLambda *lambda.Client
	AWSRDS    *rds.Client

	AzureCred           azcore.TokenCredential
	AzureSubID          string
	AzureLocation       string
	AzureRGClient       *armresources.ResourceGroupsClient
	AzureStorageAcct    *armstorage.AccountsClient
	AzureBlobContainers *armstorage.BlobContainersClient
	AzureVNetClient     *armnetwork.VirtualNetworksClient
	AzureSubnetClient   *armnetwork.SubnetsClient
	AzureNICClient      *armnetwork.InterfacesClient
	AzurePIPClient      *armnetwork.PublicIPAddressesClient
	AzureVMClient       *armcompute.VirtualMachinesClient
	AzureAKSClient      *armcontainerservice.ManagedClustersClient
	AzureWebClient      *armappservice.WebAppsClient
	AzurePlanClient     *armappservice.PlansClient
	AzureMySQLClient    *armmysqlflexibleservers.ServersClient
	AzurePostgresClient *armpostgresqlflexibleservers.ServersClient

        GCPStorage *storage.Client
        GCPCompute *compute.Service
       GCPGKE     *container.Service
       GCPFunctions *cloudfunctions.Service
       GCPCloudSQL *sqladmin.Service
       GCPProject string
       GCPRegion  string
}
