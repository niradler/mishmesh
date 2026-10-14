import argparse
import pathlib
import re
import subprocess


def create_review_package(base: str, head: str, output: pathlib.Path) -> None:
    if not all(re.fullmatch(r"[0-9a-f]{7,40}", revision) for revision in (base, head)):
        raise ValueError("base and head must be hexadecimal commit IDs")
    root = pathlib.Path(__file__).resolve().parents[3]
    destination = output.resolve()
    if not destination.is_relative_to(root / ".superpowers" / "sdd"):
        raise ValueError("review packages must stay inside this worktree's SDD directory")
    if destination.exists():
        raise FileExistsError("use a new review package path to preserve existing evidence")
    commit_range = f"{base}..{head}"
    commands = [
        ["git", "log", "--oneline", commit_range],
        ["git", "diff", "--no-ext-diff", "--no-textconv", "--stat", commit_range],
        ["git", "diff", "--no-ext-diff", "--no-textconv", "-U10", commit_range],
    ]
    results = [
        subprocess.run(
            command, cwd=root, timeout=30, capture_output=True, text=True, check=True
        )
        for command in commands
    ]
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text("\n".join(result.stdout for result in results), encoding="utf-8")
    print(destination)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("base")
    parser.add_argument("head")
    parser.add_argument("output", type=pathlib.Path)
    arguments = parser.parse_args()
    create_review_package(arguments.base, arguments.head, arguments.output)


if __name__ == "__main__":
    main()
