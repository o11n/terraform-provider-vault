package vault

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/vault/api"

	"github.com/hashicorp/terraform-provider-vault/util"
)

func awsSecretBackendRoleResource(name string) *schema.Resource {
	return &schema.Resource{
		Create: awsSecretBackendRoleWrite,
		Read:   awsSecretBackendRoleRead,
		Update: awsSecretBackendRoleWrite,
		Delete: awsSecretBackendRoleDelete,
		Exists: awsSecretBackendRoleExists,
		Importer: &schema.ResourceImporter{
			State: schema.ImportStatePassthrough,
		},

		Schema: map[string]*schema.Schema{
			"name": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Unique name for the role.",
			},
			"backend": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "The path of the AWS Secret Backend the role belongs to.",
			},
			"policy_arns": {
				Type:        schema.TypeSet,
				Optional:    true,
				Description: "ARN for an existing IAM policy the role should use.",
				Elem: &schema.Schema{
					Type: schema.TypeString,
				},
			},
			"policy_document": {
				Type:             schema.TypeString,
				Optional:         true,
				Description:      "IAM policy the role should use in JSON format.",
				ValidateFunc:     ValidateDataJSONFunc(name),
				DiffSuppressFunc: util.JsonDiffSuppress,
			},
			"credential_type": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "Role credential type.",
			},
			"role_arns": {
				Type: schema.TypeSet,
				Elem: &schema.Schema{
					Type: schema.TypeString,
				},
				Optional:    true,
				ForceNew:    true,
				Description: "ARNs of AWS roles allowed to be assumed. Only valid when credential_type is 'assumed_role'",
			},
			"iam_groups": {
				Type: schema.TypeSet,
				Elem: &schema.Schema{
					Type: schema.TypeString,
				},
				Optional:    true,
				Description: "A list of IAM group names. IAM users generated against this vault role will be added to these IAM Groups. For a credential type of assumed_role or federation_token, the policies sent to the corresponding AWS call (sts:AssumeRole or sts:GetFederation) will be the policies from each group in iam_groups combined with the policy_document and policy_arns parameters.",
			},
			"default_sts_ttl": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "The default TTL in seconds for STS credentials. When a TTL is not specified when STS credentials are requested, and a default TTL is specified on the role, then this default TTL will be used. Valid only when credential_type is one of assumed_role or federation_token.",
			},
			"max_sts_ttl": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "The max allowed TTL in seconds for STS credentials (credentials TTL are capped to max_sts_ttl). Valid only when credential_type is one of assumed_role or federation_token.",
			},
		},
	}
}

func awsSecretBackendRoleWrite(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*api.Client)

	backend := d.Get("backend").(string)
	name := d.Get("name").(string)

	policyARNsIfc, ok := d.GetOk("policy_arns")
	var policyARNs []interface{}
	if ok {
		policyARNs = policyARNsIfc.(*schema.Set).List()
	}

	policyDocument := d.Get("policy_document")

	roleARNs := d.Get("role_arns").(*schema.Set).List()

	iamGroups := d.Get("iam_groups").(*schema.Set).List()

	if policyDocument == "" && len(policyARNs) == 0 && len(roleARNs) == 0 && len(iamGroups) == 0 {
		return fmt.Errorf("at least one of: `policy_document`, `policy_arns`, `role_arns` or `iam_groups` must be set")
	}

	credentialType := d.Get("credential_type").(string)

	data := map[string]interface{}{
		"credential_type": credentialType,
	}
	if policyDocument != "" {
		data["policy_document"] = policyDocument
	}
	if len(policyARNs) != 0 {
		data["policy_arns"] = policyARNs
	}
	if len(roleARNs) != 0 {
		data["role_arns"] = roleARNs
	}
	if len(iamGroups) != 0 || !d.IsNewResource() {
		data["iam_groups"] = iamGroups
	}

	defaultStsTTL, defaultStsTTLOk := d.GetOk("default_sts_ttl")
	maxStsTTL, maxStsTTLOk := d.GetOk("max_sts_ttl")
	if credentialType == "assumed_role" || credentialType == "federation_token" {
		if defaultStsTTLOk {
			data["default_sts_ttl"] = strconv.Itoa(defaultStsTTL.(int))
		}
		if maxStsTTLOk {
			data["max_sts_ttl"] = strconv.Itoa(maxStsTTL.(int))
		}
	} else {
		if defaultStsTTLOk {
			return fmt.Errorf("default_sts_ttl is only valid when credential_type is assumed_role or federation_token")
		}
		if maxStsTTLOk {
			return fmt.Errorf("max_sts_ttl is only valid when credential_type is assumed_role or federation_token")
		}
	}

	log.Printf("[DEBUG] Creating role %q on AWS backend %q", name, backend)
	_, err := client.Logical().Write(backend+"/roles/"+name, data)
	if err != nil {
		return fmt.Errorf("error creating role %q for backend %q: %s", name, backend, err)
	}
	log.Printf("[DEBUG] Created role %q on AWS backend %q", name, backend)

	d.SetId(backend + "/roles/" + name)
	return awsSecretBackendRoleRead(d, meta)
}

func awsSecretBackendRoleRead(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*api.Client)

	path := d.Id()
	pathPieces := strings.Split(path, "/")
	if len(pathPieces) < 3 || pathPieces[len(pathPieces)-2] != "roles" {
		return fmt.Errorf("invalid id %q; must be {backend}/roles/{name}", path)
	}

	log.Printf("[DEBUG] Reading role from %q", path)
	secret, err := client.Logical().Read(path)
	if err != nil {
		return fmt.Errorf("error reading role %q: %s", path, err)
	}
	log.Printf("[DEBUG] Read role from %q", path)
	if secret == nil {
		log.Printf("[WARN] Role %q not found, removing from state", path)
		d.SetId("")
		return nil
	}

	if _, ok := d.GetOk("policy_document"); ok {
		d.Set("policy_document", secret.Data["policy_document"])
	} else if v, ok := secret.Data["policy_document"]; ok {
		d.Set("policy_document", v)
	}

	if _, ok := d.GetOk("policy_arns"); ok {
		d.Set("policy_arns", secret.Data["policy_arns"])
	} else if v, ok := secret.Data["policy_arns"]; ok {
		d.Set("policy_arns", v)
	}

	d.Set("credential_type", secret.Data["credential_type"])
	d.Set("role_arns", secret.Data["role_arns"])
	if v, ok := secret.Data["default_sts_ttl"]; ok {
		d.Set("default_sts_ttl", v)
	}
	if v, ok := secret.Data["max_sts_ttl"]; ok {
		d.Set("max_sts_ttl", v)
	}
	if v, ok := secret.Data["iam_groups"]; ok {
		d.Set("iam_groups", v)
	}
	d.Set("backend", strings.Join(pathPieces[:len(pathPieces)-2], "/"))
	d.Set("name", pathPieces[len(pathPieces)-1])
	return nil
}

func awsSecretBackendRoleDelete(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*api.Client)

	path := d.Id()
	log.Printf("[DEBUG] Deleting role %q", path)
	_, err := client.Logical().Delete(path)
	if err != nil {
		return fmt.Errorf("error deleting role %q: %s", path, err)
	}
	log.Printf("[DEBUG] Deleted role %q", path)
	return nil
}

func awsSecretBackendRoleExists(d *schema.ResourceData, meta interface{}) (bool, error) {
	client := meta.(*api.Client)

	path := d.Id()
	log.Printf("[DEBUG] Checking if %q exists", path)
	secret, err := client.Logical().Read(path)
	if err != nil {
		return true, fmt.Errorf("error checking if %q exists: %s", path, err)
	}
	log.Printf("[DEBUG] Checked if %q exists", path)
	return secret != nil, nil
}
