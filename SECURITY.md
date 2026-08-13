# Security policy

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability involving credentials, authorization bypass, secret disclosure, unsafe image fetching, or destructive infrastructure behavior.

Use the repository's private security-advisory feature or contact the maintainer through the private security address published with the repository. Include:

- Affected version or commit
- Reproduction steps
- Security impact
- Suggested mitigation, if known

Do not include live Omni service-account keys, Xen Orchestra API tokens, Talos join tokens, or other credentials.

## Supported versions

Until the project reaches a stable release, security fixes are provided for the latest published release only.

## Secret handling

Treat these values as secrets:

- `OMNI_SERVICE_ACCOUNT_KEY`
- `XOA_TOKEN`
- `XOA_PASSWORD`
- Machine Join Config and embedded join tokens

Store secrets in a container secret store, protected environment file, or Kubernetes Secret. Do not commit them to source control.

## Image Factory URL security

The Image Factory base URL is provider-level configuration rather than Machine Class data. This prevents ordinary Machine Class input from redirecting the provider to arbitrary URLs.

Operators should:

- Use HTTPS.
- Trust only controlled certificate authorities.
- Restrict outbound access from the provider container where appropriate.
- Review private Image Factory access controls.

Because the provider (not Xen Orchestra) downloads images directly, the provider host itself has outbound HTTPS access to the Image Factory — factor this into your network segmentation.

## Least privilege

Use a dedicated Xen Orchestra API user scoped to the resources required by this provider. Avoid administrator credentials for long-running deployments.
