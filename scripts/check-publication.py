"""Reject confidential files in the Git index without printing their contents."""
import pathlib
import re
import subprocess
import sys

paths = subprocess.check_output(["git", "ls-files", "-z"]).decode().split("\0")
private_dirs = {".cache", ".local", ".secrets", "secrets", ".credentials", ".aws", ".azure", ".gcloud", ".terraform", "node_modules", ".auth", ".kube"}
private_names = {".npmrc", ".pypirc", ".netrc", ".git-credentials", "id_rsa", "id_ed25519", "application_default_credentials.json"}
private_suffixes = {".pem", ".key", ".p12", ".pfx", ".cer", ".crt", ".dump", ".backup", ".log", ".sqlite", ".sqlite3", ".db", ".tfplan", ".kubeconfig"}
blocked = []
for name in filter(None, paths):
    p = pathlib.PurePosixPath(name.lower())
    if (private_dirs.intersection(p.parts) or p.name in private_names
        or p.name.startswith(".env") or p.suffix in private_suffixes
        or re.search(r"(?:credentials|service-account)\.json$|\.tfstate(?:\.|$)|\.tfvars(?:\.|$)|\.sql\.gz$", p.name)
        or name.startswith("web/dist/")):
        blocked.append(name)
if blocked:
    print("Publication blocked: confidential or generated paths are tracked:")
    print("\n".join(blocked))
    sys.exit(1)
print(f"Publication path check passed ({len(list(filter(None, paths)))} tracked files).")
