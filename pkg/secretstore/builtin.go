package secretstore

// Builtin returns the providers core ships. Each provider is one file and
// a test against its vendor's emulator or dev server (ADR-041 section 3);
// they are added here as they land.
func Builtin() []Provider {
	return []Provider{
		Vault(),
		AWSSecretsManager(),
		AWSSSM(),
	}
}
