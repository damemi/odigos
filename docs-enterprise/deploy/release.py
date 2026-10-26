"""Release a verified image through the existing stack, preserving infrastructure."""

import argparse
import json
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request


def aws(*args):
    result = subprocess.run(
        ["aws", *args, "--output", "json"], capture_output=True, text=True
    )
    if result.returncode:
        # Never print responses that might contain Lambda environment values.
        if "No updates are to be performed" in result.stderr:
            return None
        raise RuntimeError(f"AWS {args[0]} {args[1]} failed; inspect the stack events.")
    return json.loads(result.stdout) if result.stdout.strip() else {}


def stack_info(name):
    return aws("cloudformation", "describe-stacks", "--stack-name", name)["Stacks"][0]


def update_image(name, stack, image):
    parameters = [
        {
            "ParameterKey": p["ParameterKey"],
            **(
                {"ParameterValue": image}
                if p["ParameterKey"] == "ImageUri"
                else {"UsePreviousValue": True}
            ),
        }
        for p in stack["Parameters"]
    ]
    if not any(p["ParameterKey"] == "ImageUri" for p in parameters):
        raise RuntimeError("Stack has no ImageUri parameter")
    request = {
        "StackName": name,
        "UsePreviousTemplate": True,
        "Parameters": parameters,
        "Capabilities": ["CAPABILITY_IAM"],
    }
    aws("cloudformation", "update-stack", "--cli-input-json", json.dumps(request))
    aws("cloudformation", "wait", "stack-update-complete", "--stack-name", name)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args):
        return None


def verify_public_boundary(origin):
    opener = urllib.request.build_opener(NoRedirect)
    for path, expected in [
        ("/login", 200),
        ("/enterprise/overview", 401),
        ("/central/overview", 401),
        ("/docs-runtime.js", 401),
        ("/pagefind/pagefind.js", 401),
        ("/llms.txt", 401),
    ]:
        try:
            response = opener.open(origin + path, timeout=30)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            if response.status != expected:
                raise RuntimeError(
                    f"{path} returned {response.status}, expected {expected}"
                )
            if "no-store" not in response.headers.get("Cache-Control", ""):
                raise RuntimeError(f"{path} permits caching")
            if path == "/login" and b"Enterprise API key" not in response.read():
                raise RuntimeError("Login page is not the docs application")


def release(name, image):
    if not re.fullmatch(
        r"\d{12}\.dkr\.ecr\.[a-z0-9-]+\.amazonaws\.com/odigos-enterprise-docs@sha256:[a-f0-9]{64}",
        image,
    ):
        raise ValueError(
            "Use the immutable digest from the enterprise docs ECR repository"
        )
    stack = stack_info(name)
    if stack["StackStatus"] not in (
        "CREATE_COMPLETE",
        "UPDATE_COMPLETE",
        "UPDATE_ROLLBACK_COMPLETE",
    ):
        raise RuntimeError("Stack is busy or needs operator attention")
    previous = next(
        p["ParameterValue"]
        for p in stack["Parameters"]
        if p["ParameterKey"] == "ImageUri"
    )
    output = {o["OutputKey"]: o["OutputValue"] for o in stack["Outputs"]}
    function = output["FunctionName"]
    origin = output["DocsURL"]
    if origin != "https://enterprise.docs.odigos.io":
        raise RuntimeError("Refusing to release to an unexpected docs origin")
    if image != previous:
        update_image(name, stack, image)
    try:
        actual = aws("lambda", "get-function", "--function-name", function)
        if (
            actual["Code"]["ResolvedImageUri"] != image
            or actual["Configuration"]["State"] != "Active"
        ):
            raise RuntimeError("Lambda is not running the requested image")
        for attempt in range(4):
            try:
                verify_public_boundary(origin)
                break
            except (RuntimeError, urllib.error.URLError):
                if attempt == 3:
                    raise
                time.sleep(5)
    except Exception:
        if image != previous:
            print(
                "Release verification failed; restoring the previous image.", flush=True
            )
            update_image(name, stack_info(name), previous)
        raise
    print(f"Verified deployed image and public access boundary at {origin}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--stack", required=True)
    parser.add_argument("--image", required=True)
    args = parser.parse_args()
    try:
        release(args.stack, args.image)
    except Exception as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
