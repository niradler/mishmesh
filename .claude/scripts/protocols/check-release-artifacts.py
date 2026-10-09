import argparse
import hashlib
import json
import pathlib
import re
import shutil
import subprocess
import urllib.request
import zipfile


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--tag", required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    args = parser.parse_args()
    if not re.fullmatch(r"v\d+\.\d+\.\d+-beta\.\d+", args.tag):
        raise ValueError("Expected a beta release tag")
    args.output.mkdir(parents=True, exist_ok=False)
    metadata = subprocess.run(["gh", "release", "view", args.tag, "--repo", "niradler/mishmesh", "--json", "assets,isDraft"], capture_output=True, text=True, timeout=30, check=True)
    release = json.loads(metadata.stdout)
    if release["isDraft"]:
        raise RuntimeError("Release is still a draft")
    prefix = f"https://github.com/niradler/mishmesh/releases/download/{args.tag}/"
    for asset in release["assets"]:
        if pathlib.PurePath(asset["name"]).name != asset["name"] or not asset["url"].startswith(prefix):
            raise ValueError("Unexpected release asset")
        with urllib.request.urlopen(asset["url"], timeout=60) as response, (args.output / asset["name"]).open("xb") as destination:
            shutil.copyfileobj(response, destination)
    verified = []
    for line in (args.output / "checksums.txt").read_text().splitlines():
        digest, filename = line.split(maxsplit=1)
        filename = filename.lstrip("*")
        if pathlib.PurePath(filename).name != filename or not re.fullmatch(r"[a-f0-9]{64}", digest):
            raise ValueError("Invalid checksum manifest entry")
        artifact = args.output / filename
        with artifact.open("rb") as source:
            actual = hashlib.file_digest(source, "sha256").hexdigest()
        if actual != digest:
            raise RuntimeError(f"Checksum mismatch: {filename}")
        verified.append(filename)
    if len(verified) != 13 or not any("webui" in name for name in verified):
        raise RuntimeError("Expected all twelve binaries and web UI in checksums")
    versions = {}
    for component in ["server", "agent"]:
        archive = args.output / f"mishmesh-{component}_{args.tag[1:]}_windows_amd64.zip"
        executable = f"mishmesh-{component}.exe"
        with zipfile.ZipFile(archive) as bundle:
            output = args.output / executable
            output.write_bytes(bundle.read(executable))
        version = subprocess.run([str(output), "version"], capture_output=True, text=True, timeout=15, check=True).stdout.strip()
        if args.tag not in version:
            raise RuntimeError(f"Wrong {component} version: {version}")
        versions[component] = version
    summary = {"passed": True, "tag": args.tag, "anonymous_asset_downloads": True, "verified_archive_count": len(verified), "web_ui_checksummed": True, "windows_versions": versions}
    (args.output / "proof.json").write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(summary), flush=True)


if __name__ == "__main__":
    main()
