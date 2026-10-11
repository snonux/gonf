#!/usr/bin/env python3
"""Verify real CLI packaging and apply inside an automatically removed tree."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile


def run(args, env, expected=0):
    result = subprocess.run(args, env=env, text=True, capture_output=True, timeout=180)
    if result.returncode != expected:
        raise RuntimeError(f"{args}: exit {result.returncode}\n{result.stdout}{result.stderr}")
    return result.stdout + result.stderr


def verify(fixture, receiver):
    with tempfile.TemporaryDirectory(prefix="gonfy-dsl-e2e-") as temporary:
        root = Path(temporary)
        source, destination, package = root / "source", root / "lodge", root / "plan"
        cards = source / "postcards"
        (cards / "private").mkdir(parents=True)
        (source / "welcome.txt").write_text("Gonfy welcomes you\n")
        (cards / "public.txt").write_text("public postcard\n")
        (cards / "private" / "canary").write_text("EXCLUDED_PAYLOAD_CANARY")
        (cards / "draft.tmp").write_text("EXCLUDED_PAYLOAD_CANARY")
        env = {key: value for key, value in os.environ.items()
               if not key.startswith("GONF_E2E_")}
        home = root / "home"
        home.mkdir()
        env.update(HOME=str(home), XDG_CONFIG_HOME=str(home / ".config"),
                   GONF_E2E_SOURCE=str(source), GONF_E2E_DESTINATION=str(destination))
        run([fixture, "plan", "-no-default-recipients", "-plaintext", "-o", str(package), "fixture_copy", "excluded"], env)
        ops = [json.loads(line) for line in (package / "plan.jsonl").read_text().splitlines()]
        assert [op["op"] for op in ops][:3] == ["plan", "dir", "file"]
        assert not any("EXCLUDED_PAYLOAD_CANARY" in p.read_text()
                       for p in package.rglob("*") if p.is_file())
        plan_args = [receiver, "apply", str(package / "plan.jsonl")]
        dry = run([receiver, "-dry-run", *plan_args[1:]], env)
        assert "5 would-change" in dry and not destination.exists(), dry
        initial = run(plan_args, env)
        assert "5 changed" in initial, initial
        assert not (destination / "must-not-exist").exists()
        assert (destination / "lodge.conf").read_text() == (
            'GONFY_NAME="Gonfy the beaver"\nGONFY_ENABLED="YES"\n')
        assert (destination / "welcome.txt").read_text() == "Gonfy welcomes you\n"
        assert not (destination / "postcards" / "private").exists()
        repeat = run(plan_args, env)
        assert "0 changed" in repeat and "5 ok" in repeat, repeat
        # Included drift is repaired; excluded destination bytes remain unmanaged.
        (destination / "postcards" / "public.txt").write_text("drift\n")
        (destination / "postcards" / "private").mkdir()
        protected = destination / "postcards" / "private" / "local"
        protected.write_text("Gonfy's private postcard\n")
        repaired = run(plan_args, env)
        assert "1 changed" in repaired, repaired
        assert protected.read_text() == "Gonfy's private postcard\n"
        assert (destination / "postcards" / "public.txt").read_text() == "public postcard\n"
        # Packaged bytes survive removal of their controller source.
        (source / "welcome.txt").unlink()
        (destination / "welcome.txt").unlink()
        assert "1 changed" in run(plan_args, env)
        assert (destination / "welcome.txt").read_text() == "Gonfy welcomes you\n"
        assert "0 changed" in run(plan_args, env)
        negative = run([fixture, "plan", "-no-default-recipients", "-plaintext", "-o", str(root / "refused"), "fixture_copy"], env, 1)
        assert "Gonfy's welcome postcard" in negative and "create the fixture source first" in negative
        assert not (root / "refused" / "plan.jsonl").exists()
        # Optional absence skips the directory declaration; wrong type refuses it.
        (source / "welcome.txt").write_text("Gonfy welcomes you\n")
        cards.rename(source / "saved-postcards")
        optional = root / "optional-absent"
        run([fixture, "plan", "-no-default-recipients", "-plaintext", "-o",
             str(optional), "fixture_copy"], env)
        optional_ops = [json.loads(line) for line in
                        (optional / "plan.jsonl").read_text().splitlines()]
        assert not any(op["op"] == "sync_dir" for op in optional_ops)
        cards.write_text("wrong source type\n")
        wrong_type = run([fixture, "plan", "-no-default-recipients", "-plaintext",
                          "-o", str(root / "wrong-type"), "fixture_copy"], env, 1)
        assert "optional postcards" in wrong_type
        assert not (root / "wrong-type" / "plan.jsonl").exists()
        print("PASS: package filtering, dry-run, dependencies, guard, convergence, drift, preservation, missing-source and wrong-type refusal")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("fixture", type=os.path.abspath)
    parser.add_argument("receiver", type=os.path.abspath)
    args = parser.parse_args()
    verify(args.fixture, args.receiver)
