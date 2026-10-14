import argparse
import json
from pathlib import Path
import subprocess
import sys


def main():
    parser = argparse.ArgumentParser(description="Run a browser scenario against the retained isolated real API fixture")
    parser.add_argument("scenario")
    parser.add_argument("--session", default="mishmesh-access-real")
    options = parser.parse_args()
    root = Path(__file__).resolve().parents[3]
    metadata = json.loads((root / "output/browser-real/fixture.json").read_text(encoding="utf-8"))
    public = {key: metadata[key] for key in ("api", "ingress", "python", "directory")}
    evidence = {}
    for scenario in ("services", "access", "guards", "shipping-forms"):
        log = root / "output/browser-real" / ("check-real-" + scenario + ".log")
        if log.exists():
            content = log.read_text(encoding="utf-8")
            if "### Result\n" in content:
                evidence[scenario] = json.loads(content.split("### Result\n", 1)[1].split("\n", 1)[0])
    public["evidence"] = evidence
    prefix = ["npx.cmd", "--yes", "--package", "@playwright/cli@0.1.22", "playwright-cli", "-s=" + options.session]
    injection = "async (page) => { const data = " + json.dumps(public) + "; await page.addInitScript(value => window.__mishmeshFixture = value, data); await page.evaluate(value => window.__mishmeshFixture = value, data); }"
    subprocess.run(prefix + ["run-code", injection], cwd=root, timeout=30, check=True, capture_output=True, text=True)
    scenario = root / ".claude/scripts/pluggable-types" / options.scenario
    if scenario.parent != root / ".claude/scripts/pluggable-types" or not scenario.is_file():
        raise RuntimeError("choose an existing scenario file in the validation script directory")
    result = subprocess.run(prefix + ["run-code", "--filename=" + str(scenario)], cwd=root, timeout=180, capture_output=True, text=True)
    output = result.stdout + result.stderr
    (root / "output/browser-real" / (scenario.stem + ".log")).write_text(output, encoding="utf-8")
    print(output, end="")
    return result.returncode


if __name__ == "__main__":
    sys.exit(main())
