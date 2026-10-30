# May Route Core

The paired sing-box core source for [May Route](https://github.com/mxh110708/may-route-desktop), covering protocol processing and Windows service/local-IPC integration. Desktop behavior, installer, updates and failover policy are maintained in the desktop repository.

[简体中文](README.md) · [Integration guide](docs/MAY-CORE.zh-CN.md) · [Upstream protocol reference](https://sing-box.sagernet.org)

Unreleased source extends native VLESS + TLS/Reality + XHTTP outbounds with four modes, valid HTTP-version combinations, independent download identities and advanced fields. See the [May XHTTP guide](docs/MAY-XHTTP.zh-CN.md) for configuration, pooling policy and isolated acceptance limits. Installed builds and public CDN compatibility are not established by this source update.

Use the exact core revision and toolchain selected by the desktop build record. The current documentation baseline is desktop 1.14.2-may.26 on `custom-main`. End users download the complete desktop installer; this repository has no separate May desktop product release.

Retained Go module/protocol identifiers are technical compatibility and attribution, not unfinished branding. The upstream-derived `docs/` tree is a core syntax reference, not proof that every protocol, platform or optional feature has a supported May desktop workflow. Server deployment belongs to [May VPS Deploy](https://github.com/mxh110708/may-vps-deploy).

Derived from [SagerNet/sing-box](https://github.com/SagerNet/sing-box), not an official SagerNet release. Preserve [license, attribution and naming conditions](LICENSE). Keep private profiles, credentials and signing material outside this repository.
