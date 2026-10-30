package forgetest

import "github.com/jeffzi/ils/internal/forge"

// Check returns a check of the ci workflow named name, in bucket, that the trunk does not require.
// It carries no link.
func Check(name string, bucket forge.Bucket) forge.Check {
	return forge.Check{Name: name, Bucket: bucket, Workflow: "ci"}
}

// RequiredCheck returns Check(name, bucket) as a check the trunk requires.
func RequiredCheck(name string, bucket forge.Bucket) forge.Check {
	check := Check(name, bucket)
	check.Required = true

	return check
}
