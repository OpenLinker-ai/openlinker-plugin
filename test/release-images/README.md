# Provider release-image evidence

This release-only verifier rejects a Provider, Browser, or Egress image unless
its published OCI index contains exactly one `linux/amd64` image, exactly one
`linux/arm64` image, and an attestation manifest for each platform containing
both an SPDX SBOM and SLSA provenance.

It verifies already-pushed immutable release output; build configuration alone
is not accepted as release evidence.

```sh
./test/release-images/verify.sh ghcr.io/openlinker-ai vX.Y.Z
```

Set `OPENLINKER_RELEASE_EVIDENCE_DIR` to save the inspected OCI index and
attestation descriptors. The tag workflow runs this verifier after all four
images are pushed and uploads that directory as the release evidence artifact.
`verify.test.sh` also proves that an image missing its SPDX SBOM is rejected.
