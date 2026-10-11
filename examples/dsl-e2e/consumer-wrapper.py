#!/usr/bin/env python3
"""Copy a recipe CLI into a temporary delivery wrapper without editing its source."""
import argparse
from pathlib import Path


def wrap(source, output):
    text = source.read_text()
    cli_import = '"github.com/snonux/gonf/cli"'
    if text.count(cli_import) != 1 or text.count("cli.Main()") != 1:
        raise ValueError("expected one gonf CLI import and one cli.Main() call")
    prefix = "" if '. "github.com/snonux/gonf/api"' in text else "api."
    imports = [path for path in ("context", "fmt", "os", "time",
                                "github.com/snonux/gonf/resource")
               if f'"{path}"' not in text]
    text = text.replace(cli_import, "\n".join(f'"{path}"' for path in imports))
    text = text.replace("cli.Main()", "liveDelivery()")
    helper = '''
func liveDelivery() {
 ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
 defer cancel()
 target := PREFIXPushTarget{Host: os.Getenv("L93_HOST"), User: os.Getenv("L93_USER"),
  Port: 22, GonfPath: "/tmp/gonf-l93-receiver"}
 if os.Getenv("L93_PORT") == "2" { target.Port = 2 }
 switch os.Getenv("L93_PRIVILEGE") {
 case "doas": target.Privilege = PREFIXPrivilegeDoas
 case "sudo": target.Privilege = PREFIXPrivilegeSudo
 }
 resource.SetDryRun(os.Getenv("L93_DRY_RUN") == "1")
 var err error
 switch os.Getenv("L93_DELIVERY") {
 case "preview": err = PREFIXPreviewToContext(ctx, target, "l93-consumer", os.Args[1:]...)
 case "push": err = PREFIXPushToContext(ctx, target, "l93-consumer", os.Args[1:]...)
 default: err = fmt.Errorf("set L93_DELIVERY to preview or push")
 }
 if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
}
'''.replace("PREFIX", prefix)
    # Exclusive creation avoids replacing a caller's existing temporary artifact.
    with output.open("x") as destination:
        destination.write(text + helper)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    wrap(args.source, args.output)
