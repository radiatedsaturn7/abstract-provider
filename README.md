# Abstract Terraform Provider

This repository contains an in-progress implementation of the provider described
in `designdoc`. The project aims to expose cloud-agnostic resources so that a
single Terraform configuration can deploy to AWS, Azure, or GCP.

The provider uses the Terraform Plugin Framework and includes real support for
multiple AWS and Azure services. Buckets are provisioned with AWS S3, Azure Blob
Storage, and now Google Cloud Storage. Networks create a VPC via the AWS EC2 API,
a Virtual Network on Azure, or a VPC Network on GCP. Instances launch using EC2, Azure VMs, or Compute Engine,
functions run on AWS Lambda, clusters use EKS, AKS, or GKE, and databases are created
with RDS or Azure flexible servers. GCP support currently includes buckets,
networks, instances, clusters, Cloud Functions, and Cloud SQL databases.

## Building

Before building or testing, run `scripts/setup.sh` to download Go module dependencies. This script respects the `GOPROXY` environment variable, so you can point it at an alternate module proxy if the default `proxy.golang.org` is blocked. If module downloads continue to fail, fetch them on a machine with access and commit the resulting `vendor` directory.


```
go build
```

## Continuous Integration

Builds are validated in GitHub Actions. The workflow in
`.github/workflows/build.yml` compiles the provider on each push and pull
request to ensure the code builds successfully.

## Usage

Build the provider and place the resulting binary in your Terraform plugin
directory. Resources like `abstract_bucket` and `abstract_network` will create
real AWS infrastructure when the `type` attribute is set to `"aws"`.

Other resource types are currently placeholders but will be implemented following
the design document.

### Instance sizes

When using `abstract_instance`, the `size` attribute accepts generic values
`small`, `medium`, or `large`. These map to equivalent machine types in each
cloud:

- AWS: `t3.small`, `t3.medium`, `t3.large`
- Azure: `Standard_B1s`, `Standard_B2s`, `Standard_B4ms`
- GCP: `e2-small`, `e2-medium`, `e2-standard-4`

You can still provide a cloud-specific instance type directly by specifying the
exact value in the `size` field.

### Naming requirements

Resource names must satisfy the strictest rules across providers. Bucket names, for example, must be DNS compatible and globally unique. Function names have length and character restrictions that vary per cloud. Refer to `designdoc` for details when choosing names.

### Function packaging

`abstract_function` resources expect your code to be packaged in the format required by each cloud (ZIP for AWS and GCP, a function app package for Azure). Ensure the package includes any handler files referenced in the configuration before applying.
