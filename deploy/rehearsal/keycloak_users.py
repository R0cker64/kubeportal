"""Creates the demo groups and users of the install rehearsal in the
kubeportal realm through the Keycloak admin API, like an administrator would
in the console. Idempotent.

Usage: keycloak_users.py <keycloak-url> <admin-user> <admin-password> <demo-password>
"""

import json
import sys
import urllib.error
import urllib.parse
import urllib.request

GROUPS = ["kubeportal-admin", "team-payments", "team-catalog", "team-search"]
USERS = {
    "admin": "kubeportal-admin",
    "alice": "team-payments",
    "bob": "team-catalog",
    "dave": "team-search",
    "carol": None,
}


def request(method, url, token=None, body=None, form=None):
    data, headers = None, {}
    if form is not None:
        data = urllib.parse.urlencode(form).encode()
        headers["Content-Type"] = "application/x-www-form-urlencoded"
    elif body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    with urllib.request.urlopen(req, timeout=30) as resp:  # noqa: S310 - local URL
        raw = resp.read()
        return json.loads(raw) if raw else None


def main():
    base, admin_user, admin_password, demo_password = sys.argv[1:5]
    token = request("POST", base + "/realms/master/protocol/openid-connect/token", form={
        "client_id": "admin-cli", "grant_type": "password",
        "username": admin_user, "password": admin_password,
    })["access_token"]
    realm = base + "/admin/realms/kubeportal"

    existing = {g["name"]: g["id"] for g in request("GET", realm + "/groups", token)}
    for name in GROUPS:
        if name not in existing:
            request("POST", realm + "/groups", token, {"name": name})
    groups = {g["name"]: g["id"] for g in request("GET", realm + "/groups", token)}

    for username, group in USERS.items():
        found = request("GET", realm + "/users?exact=true&username=" + username, token)
        if not found:
            request("POST", realm + "/users", token, {
                "username": username, "enabled": True, "emailVerified": True,
                "firstName": username.capitalize(), "lastName": "Demo",
                "email": username + "@rehearsal.local",
                "credentials": [{"type": "password", "value": demo_password, "temporary": False}],
            })
            found = request("GET", realm + "/users?exact=true&username=" + username, token)
        if group:
            request("PUT", f"{realm}/users/{found[0]['id']}/groups/{groups[group]}", token)
    print("keycloak: groups", ", ".join(GROUPS), "and users", ", ".join(USERS))


if __name__ == "__main__":
    try:
        main()
    except urllib.error.HTTPError as err:
        sys.exit(f"keycloak admin API error: {err.code} {err.reason}")
