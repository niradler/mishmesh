import json
import re
import subprocess
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
CHART = ROOT / "deploy/helm/mishmesh"


def render(arguments):
    output = subprocess.run(
        ["helm", "template", "security-check", str(CHART), *arguments],
        check=True,
        capture_output=True,
        text=True,
        timeout=30,
    ).stdout
    return {
        name: json.loads(value)
        for name, value in re.findall(
            r"- name: (MISHMESH_\w+)\s+value: (\"[^\n]*\")", output
        )
    }


def main():
    defaults = {
        "MISHMESH_PROXY_ALLOW_PRIVATE": "false",
        "MISHMESH_PROXY_ALLOWED_CIDRS": "",
        "MISHMESH_ALLOWED_ORIGINS": "",
        "MISHMESH_MAX_ORGS_PER_USER": "3",
        "MISHMESH_UPSTREAM_RESPONSE_TIMEOUT": "5m",
        "MISHMESH_TLS_PASSTHROUGH_PUBLIC_PORT": "8444",
    }
    for profile in [None, "company", "saas"]:
        arguments = [] if profile is None else ["-f", str(CHART / f"values-{profile}.yaml")]
        values = render(arguments)
        for name, expected in defaults.items():
            assert values[name] == expected, (profile, name, values[name])
        assert "MISHMESH_PATH_ROUTING" not in values, profile
        print(f"PASS {profile or 'homelab'} security defaults and automatic path routing")
    overrides = render([
        "--set", "features.pathRouting=false",
        "--set", "features.proxyAllowPrivate=true",
        "--set", "features.proxyAllowedCIDRs[0]=10.20.0.0/16",
        "--set", "features.proxyAllowedCIDRs[1]=192.168.5.0/24",
        "--set", "auth.allowedOrigins[0]=http://localhost:5173",
        "--set", "auth.allowedOrigins[1]=https://control.example.com",
        "--set", "auth.maxOrgsPerUser=7",
        "--set", "upstreamResponseTimeout=4m",
        "--set", "listeners.tlsPassthrough.servicePort=9443",
    ])
    expected = {
        "MISHMESH_PATH_ROUTING": "false",
        "MISHMESH_PROXY_ALLOW_PRIVATE": "true",
        "MISHMESH_PROXY_ALLOWED_CIDRS": "10.20.0.0/16,192.168.5.0/24",
        "MISHMESH_ALLOWED_ORIGINS": "http://localhost:5173,https://control.example.com",
        "MISHMESH_MAX_ORGS_PER_USER": "7",
        "MISHMESH_UPSTREAM_RESPONSE_TIMEOUT": "4m",
        "MISHMESH_TLS_PASSTHROUGH_PUBLIC_PORT": "9443",
    }
    for name, value in expected.items():
        assert overrides[name] == value, (name, overrides[name])
    assert render(["--set", "features.pathRouting=true"])["MISHMESH_PATH_ROUTING"] == "true"
    print("PASS explicit booleans, joined allowlists, origins, and organization cap")


if __name__ == "__main__":
    main()
