# Corporate CA certificates

If your network intercepts TLS (Zscaler, Netskope, etc. — the symptom is
`CERTIFICATE_VERIFY_FAILED: unable to get local issuer certificate` during
`pip install` or `git clone` inside a container), drop the corporate root
CA(s) here as PEM files with a **`.crt`** extension. The Dockerfiles copy
everything in this directory into the container trust store; when the
directory is empty this is a no-op.

## Export the CA

**macOS** — exports every root your Mac's System keychain trusts (includes
the corporate one) as one PEM bundle:

```bash
security find-certificate -a -p /Library/Keychains/System.keychain > deploy/certs/corporate-ca.crt
```

**Windows (PowerShell)** — or use `certmgr.msc` → Trusted Root Certification
Authorities → find the corporate CA → Export as Base-64 X.509:

```powershell
$ca = Get-ChildItem Cert:\LocalMachine\Root | Where-Object Subject -like "*YourCompany*"
"-----BEGIN CERTIFICATE-----`n" + [Convert]::ToBase64String($ca.RawData, "InsertLineBreaks") + "`n-----END CERTIFICATE-----" |
  Set-Content deploy\certs\corporate-ca.crt
```

Then rebuild — the changed COPY layer invalidates the cache automatically:

```bash
docker compose up -d --build
```

`.crt` files here are gitignored: they're public certificates, not secrets,
but they're specific to your network and don't belong in the repo.

**Alternative:** build once from a non-corporate network (hotspot); the
certs are only needed while the *build* runs behind the intercepting proxy —
but note the indexer also clones over HTTPS at *runtime*, so on a corporate
network you want the CA baked in regardless.
