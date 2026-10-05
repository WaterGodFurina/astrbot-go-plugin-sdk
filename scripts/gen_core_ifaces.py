#!/usr/bin/env python3
"""Keep the gRPC transport adapter in sync with the native SDK interfaces.

Historically this script GENERATED both:
  * `service_iface.go`  - the core sdk.PluginService / sdk.HostService
  * `transport/grpc/host_adapter.go` - the reverse-call adapter

That is no longer the case. The core interfaces are now HAND-WRITTEN
(`service_iface.go`, `client_iface.go`) and are the source of truth; the core
package must not import protobuf. The gRPC transport's `host_adapter.go`
implements the native `sdk.HostService` by converting native values to/from the
protobuf wire types. Those conversions are type-specific (JSON fields, component
chains, map/any), so they are maintained alongside the interface rather than
generated from it.

This script therefore no longer writes `service_iface.go`. It validates that the
hand-written files exist and that the transport still compiles, so running it is
safe and cannot break the build.
"""
import os
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

HANDWRITTEN = [
    "service_iface.go",
    "client_iface.go",
    "native_types.go",
    os.path.join("transport", "grpc", "host_adapter.go"),
]

missing = [p for p in HANDWRITTEN if not os.path.exists(os.path.join(ROOT, p))]
if missing:
    sys.exit("missing hand-written file(s): %s" % ", ".join(missing))

result = subprocess.run(["go", "build", "./transport/..."], cwd=ROOT)
if result.returncode != 0:
    sys.exit("transport build failed; fix host_adapter.go to match sdk.HostService")

print("service_iface.go is hand-written; host_adapter.go matches sdk.HostService (transport builds)")
