package provider_test

import (
    "fmt"
    "os"
    "testing"
    "time"

    "github.com/hashicorp/terraform-plugin-testing/helper/resource"
    "abstract-provider/provider"
)

func TestAccBucketAWS(t *testing.T) {
    if os.Getenv("AWS_ACCESS_KEY_ID") == "" || os.Getenv("AWS_SECRET_ACCESS_KEY") == "" {
        t.Skip("AWS credentials not set")
    }

    name := fmt.Sprintf("tf-acc-%d", time.Now().UnixNano())

    resource.Test(t, resource.TestCase{
        ProtoV6ProviderFactories: map[string]func() (resource.Provider, error){
            "abstract": provider.New,
        },
        Steps: []resource.TestStep{
            {
                Config: testAccBucketAWSConfig(name),
                Check: resource.ComposeAggregateTestCheckFunc(
                    resource.TestCheckResourceAttr("abstract_bucket.test", "name", name),
                    resource.TestCheckResourceAttr("abstract_bucket.test", "type", "aws"),
                ),
            },
            {
                ResourceName:      "abstract_bucket.test",
                ImportState:       true,
                ImportStateVerify: true,
            },
        },
    })
}

func testAccBucketAWSConfig(name string) string {
    return fmt.Sprintf(`
provider "abstract" {
  aws = {
    region = "us-east-1"
  }
}

resource "abstract_bucket" "test" {
  name = "%s"
  type = "aws"
}
`, name)
}

