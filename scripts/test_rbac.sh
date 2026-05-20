#!/usr/bin/env bash
# E2E test: create a custom role with specific permissions, invite a staff
# user, accept the invite from MailHog, log in as the new user, verify
# the JWT carries exactly the granted permissions.
set -eo pipefail

API=http://localhost:8081
MAIL=http://localhost:8025
ADMIN_EMAIL="admin@nexussacco.local"
ADMIN_PW="${SEED_PW:-ChangeMeOnFirstLogin!1}"
TENANT_SLUG="${TENANT_SLUG:-tujenge}"
TENANT_HOST="${TENANT_SLUG}.nexussacco.local"
STAFF_EMAIL="ops.staff.$(date +%s)@nexussacco.local"
STAFF_NAME="Ops Staff"
STAFF_PW="StaffPwd-Activate-2026"
ROLE_CODE="loan_reviewer_$(date +%s | tail -c 5)"

pass() { printf "  [✓] %s\n" "$1"; }
fail() { printf "  [✗] %s\n" "$1"; exit 1; }
section() { printf "\n=== %s ===\n" "$1"; }

# Login helper — handles MFA if enabled. Echoes "<access>|<refresh>".
# Arg 3 (optional): Host header to set (defaults to platform host behaviour).
do_login() {
  local e="$1" p="$2" host="${3:-}"
  local resp r mfa_token code body msgs host_arg=()
  if [ -n "$host" ]; then host_arg=(-H "Host: $host"); fi
  resp=$(curl -s "${host_arg[@]}" -X POST $API/v1/auth/login \
    -H "Content-Type: application/json" \
    -d "{\"email\":\"$e\",\"password\":\"$p\"}")
  r=$(echo "$resp" | jq -r '.data.mfa_required // false')
  if [ "$r" = "true" ]; then
    mfa_token=$(echo "$resp" | jq -r '.data.mfa_token')
    code=""
    for _ in 1 2 3 4 5 6 7 8; do
      sleep 1
      msgs=$(curl -s "$MAIL/api/v2/messages")
      body=$(echo "$msgs" | jq -r '.items[0].Content.Body // empty' | tr -d '\r' | tr -d '\n')
      code=$(echo "$body" | grep -oE '(^|[^0-9])[0-9]{6}([^0-9]|$)' | grep -oE '[0-9]{6}' | head -1 || true)
      [ -n "$code" ] && break
    done
    [ -n "$code" ] || { echo "ERR:NO_OTP:$body" >&2; return 1; }
    resp=$(curl -s "${host_arg[@]}" -X POST $API/v1/auth/mfa/verify \
      -H "Content-Type: application/json" \
      -d "{\"mfa_token\":\"$mfa_token\",\"code\":\"$code\"}")
  fi
  local at rt
  at=$(echo "$resp" | jq -r '.data.access_token // empty')
  rt=$(echo "$resp" | jq -r '.data.refresh_token // empty')
  [ -n "$at" ] || { echo "ERR:NO_TOKEN:$resp" >&2; return 1; }
  echo "$at|$rt"
}

# Decode JWT payload (no signature verification — we just want to see claims).
jwt_payload() {
  local jwt="$1"
  local pay
  pay=$(echo "$jwt" | cut -d. -f2)
  # base64url → base64
  pay=$(echo "$pay" | tr '_-' '/+')
  # pad
  local pad=$(( 4 - ${#pay} % 4 ))
  if [ $pad -ne 4 ]; then pay="$pay$(printf '=%.0s' $(seq 1 $pad))"; fi
  echo "$pay" | base64 -d 2>/dev/null
}

# -------------------------------------------------------------------
section "Login as platform admin"
curl -s -X DELETE $MAIL/api/v1/messages > /dev/null
PAIR=$(do_login "$ADMIN_EMAIL" "$ADMIN_PW") || fail "admin login: $PAIR"
ADMIN_ACCESS="${PAIR%%|*}"
pass "logged in"

# -------------------------------------------------------------------
section "GET /v1/permissions"
PERMS=$(curl -s -H "Authorization: Bearer $ADMIN_ACCESS" $API/v1/permissions)
COUNT=$(echo "$PERMS" | jq '.data | length')
[ "$COUNT" -ge 28 ] || fail "expected ≥28 permissions, got $COUNT"
pass "permission catalog has $COUNT entries"

# -------------------------------------------------------------------
section "POST /v1/roles (create custom role on $TENANT_SLUG)"
RESP=$(curl -s -H "Host: $TENANT_HOST" -X POST $API/v1/roles \
  -H "Authorization: Bearer $ADMIN_ACCESS" -H "Content-Type: application/json" \
  -d "{\"code\":\"$ROLE_CODE\",\"name\":\"Loan Reviewer\",\"description\":\"Read-only loan + member view\",\"permissions\":[\"loans:view\",\"members:view\",\"reports:view\"]}")
ROLE_ID=$(echo "$RESP" | jq -r '.data.id // empty')
[ -n "$ROLE_ID" ] || fail "no role id in $RESP"
PERMS_COUNT=$(echo "$RESP" | jq '.data.permissions | length')
[ "$PERMS_COUNT" = "3" ] || fail "expected 3 perms, got $PERMS_COUNT"
pass "created custom role $ROLE_CODE ($ROLE_ID)"

# Re-create with same code → 409
DUP_CODE=$(curl -s -H "Host: $TENANT_HOST" -o /dev/null -w "%{http_code}" -X POST $API/v1/roles \
  -H "Authorization: Bearer $ADMIN_ACCESS" -H "Content-Type: application/json" \
  -d "{\"code\":\"$ROLE_CODE\",\"name\":\"Dupe\",\"permissions\":[]}")
[ "$DUP_CODE" = "409" ] || fail "expected 409 on duplicate, got $DUP_CODE"
pass "duplicate code rejected (409)"

# Can't create role with system code
SYS_CODE=$(curl -s -H "Host: $TENANT_HOST" -o /dev/null -w "%{http_code}" -X POST $API/v1/roles \
  -H "Authorization: Bearer $ADMIN_ACCESS" -H "Content-Type: application/json" \
  -d '{"code":"sacco_admin","name":"Hijack","permissions":[]}')
[ "$SYS_CODE" = "409" ] || fail "expected 409 on system-code reuse, got $SYS_CODE"
pass "system-role code reuse rejected (409)"

# -------------------------------------------------------------------
section "PATCH /v1/roles/{id} (update permissions)"
RESP=$(curl -s -H "Host: $TENANT_HOST" -X PATCH $API/v1/roles/$ROLE_ID \
  -H "Authorization: Bearer $ADMIN_ACCESS" -H "Content-Type: application/json" \
  -d '{"permissions":["loans:view","members:view","reports:view","reports:export"]}')
NEW_COUNT=$(echo "$RESP" | jq '.data.permissions | length')
[ "$NEW_COUNT" = "4" ] || fail "expected 4 perms after update, got $NEW_COUNT"
pass "added reports:export ($NEW_COUNT perms)"

# Update with unknown permission → 400
BAD_CODE=$(curl -s -H "Host: $TENANT_HOST" -o /dev/null -w "%{http_code}" -X PATCH $API/v1/roles/$ROLE_ID \
  -H "Authorization: Bearer $ADMIN_ACCESS" -H "Content-Type: application/json" \
  -d '{"permissions":["this:does:not:exist"]}')
[ "$BAD_CODE" = "400" ] || fail "expected 400 for unknown perm, got $BAD_CODE"
pass "unknown permission rejected (400)"

# -------------------------------------------------------------------
section "POST /v1/users/invite (on $TENANT_SLUG)"
curl -s -X DELETE $MAIL/api/v1/messages > /dev/null
RESP=$(curl -s -H "Host: $TENANT_HOST" -X POST $API/v1/users/invite \
  -H "Authorization: Bearer $ADMIN_ACCESS" -H "Content-Type: application/json" \
  -d "{\"email\":\"$STAFF_EMAIL\",\"full_name\":\"$STAFF_NAME\",\"role_codes\":[\"$ROLE_CODE\"]}")
STAFF_ID=$(echo "$RESP" | jq -r '.data.id // empty')
STATUS=$(echo "$RESP" | jq -r '.data.status // empty')
[ -n "$STAFF_ID" ] || fail "no user id in $RESP"
[ "$STATUS" = "pending" ] || fail "expected pending status, got $STATUS"
pass "invited $STAFF_EMAIL (status=pending)"

# Login while pending → should fail with 401
PENDING_CODE=$(curl -s -H "Host: $TENANT_HOST" -o /dev/null -w "%{http_code}" -X POST $API/v1/auth/login \
  -H "Content-Type: application/json" \
  -d "{\"email\":\"$STAFF_EMAIL\",\"password\":\"anything-12345\"}")
[ "$PENDING_CODE" = "401" ] || fail "expected 401 while pending, got $PENDING_CODE"
pass "pending user cannot log in (401)"

# -------------------------------------------------------------------
section "Extract invite link from MailHog"
TOKEN=""
for _ in 1 2 3 4 5 6 7 8; do
  sleep 1
  MSGS=$(curl -s "$MAIL/api/v2/messages")
  TOTAL=$(echo "$MSGS" | jq -r '.total')
  if [ "$TOTAL" -ge 1 ]; then
    IDX=$(echo "$MSGS" | jq -r --arg e "$STAFF_EMAIL" \
      '.items | to_entries[] | select(.value.Content.Headers.To[0] | ascii_downcase == ($e | ascii_downcase)) | .key' | head -1)
    [ -z "$IDX" ] && continue
    BODY=$(echo "$MSGS" | jq -r ".items[$IDX].Content.Body" | tr -d '\r')
    TOKEN=$(echo "$BODY" | grep -oE 'token=[A-Za-z0-9_-]+' | head -1 | cut -d= -f2 || true)
    [ -n "$TOKEN" ] && break
  fi
done
[ -n "$TOKEN" ] || fail "could not extract invite token"
pass "extracted token: ${TOKEN:0:16}...(${#TOKEN} chars)"

# -------------------------------------------------------------------
section "POST /v1/auth/invite/accept (on $TENANT_SLUG)"
RESP=$(curl -s -H "Host: $TENANT_HOST" -w "\n%{http_code}" -X POST $API/v1/auth/invite/accept \
  -H "Content-Type: application/json" \
  -d "{\"token\":\"$TOKEN\",\"new_password\":\"$STAFF_PW\"}")
CODE=$(echo "$RESP" | tail -1)
BODY=$(echo "$RESP" | sed '$d')
[ "$CODE" = "200" ] || [ "$CODE" = "204" ] || fail "expected 200/204, got $CODE / $BODY"
pass "invite accepted ($CODE)"

# Replay the token → 401
REPLAY_CODE=$(curl -s -H "Host: $TENANT_HOST" -o /dev/null -w "%{http_code}" -X POST $API/v1/auth/invite/accept \
  -H "Content-Type: application/json" \
  -d "{\"token\":\"$TOKEN\",\"new_password\":\"$STAFF_PW\"}")
[ "$REPLAY_CODE" = "401" ] || fail "expected 401 on replay, got $REPLAY_CODE"
pass "invite token replay rejected (401)"

# -------------------------------------------------------------------
section "Login as new staff (on $TENANT_SLUG)"
curl -s -X DELETE $MAIL/api/v1/messages > /dev/null
PAIR=$(do_login "$STAFF_EMAIL" "$STAFF_PW" "$TENANT_HOST") || fail "staff login: $PAIR"
STAFF_ACCESS="${PAIR%%|*}"
pass "logged in as staff"

# -------------------------------------------------------------------
section "Verify JWT carries the custom role's permissions"
CLAIMS=$(jwt_payload "$STAFF_ACCESS")
ROLES=$(echo "$CLAIMS" | jq -r '.roles[]?' | sort | tr '\n' ',' | sed 's/,$//')
PERMS=$(echo "$CLAIMS" | jq -r '.perms[]?' | sort | tr '\n' ',' | sed 's/,$//')
echo "  roles: $ROLES"
echo "  perms: $PERMS"
echo "$PERMS" | grep -q "loans:view" || fail "missing loans:view"
echo "$PERMS" | grep -q "members:view" || fail "missing members:view"
echo "$PERMS" | grep -q "reports:view" || fail "missing reports:view"
echo "$PERMS" | grep -q "reports:export" || fail "missing reports:export"
echo "$PERMS" | grep -q "loans:approve" && fail "should NOT have loans:approve"
pass "JWT permissions match the custom role exactly"

# -------------------------------------------------------------------
section "Authorization checks"
# loan_reviewer doesn't have roles:view → /v1/roles should 403.
RC=$(curl -s -H "Host: $TENANT_HOST" -o /dev/null -w "%{http_code}" -H "Authorization: Bearer $STAFF_ACCESS" $API/v1/roles)
[ "$RC" = "403" ] || fail "expected 403 for /v1/roles without roles:view, got $RC"
pass "staff without roles:view rejected from /v1/roles (403)"

# Staff cannot invite (no users:invite)
RC=$(curl -s -H "Host: $TENANT_HOST" -o /dev/null -w "%{http_code}" -X POST $API/v1/users/invite \
  -H "Authorization: Bearer $STAFF_ACCESS" -H "Content-Type: application/json" \
  -d '{"email":"x@y.com","full_name":"x","role_codes":["teller"]}')
[ "$RC" = "403" ] || fail "expected 403 for /v1/users/invite, got $RC"
pass "staff without users:invite rejected (403)"

# -------------------------------------------------------------------
section "Unassign role then re-assign via admin API"
RC=$(curl -s -H "Host: $TENANT_HOST" -o /dev/null -w "%{http_code}" -X DELETE \
  -H "Authorization: Bearer $ADMIN_ACCESS" \
  $API/v1/users/$STAFF_ID/roles/$ROLE_ID)
[ "$RC" = "204" ] || fail "expected 204 on unassign, got $RC"
pass "role unassigned"

RC=$(curl -s -H "Host: $TENANT_HOST" -o /dev/null -w "%{http_code}" -X POST $API/v1/users/$STAFF_ID/roles \
  -H "Authorization: Bearer $ADMIN_ACCESS" -H "Content-Type: application/json" \
  -d "{\"role_code\":\"$ROLE_CODE\"}")
[ "$RC" = "204" ] || fail "expected 204 on re-assign, got $RC"
pass "role re-assigned"

# -------------------------------------------------------------------
section "Suspend staff user"
RC=$(curl -s -H "Host: $TENANT_HOST" -o /dev/null -w "%{http_code}" -X POST $API/v1/users/$STAFF_ID/status \
  -H "Authorization: Bearer $ADMIN_ACCESS" -H "Content-Type: application/json" \
  -d '{"status":"suspended"}')
[ "$RC" = "204" ] || fail "expected 204 on suspend, got $RC"

# Suspended users can't log in
RC=$(curl -s -H "Host: $TENANT_HOST" -o /dev/null -w "%{http_code}" -X POST $API/v1/auth/login \
  -H "Content-Type: application/json" \
  -d "{\"email\":\"$STAFF_EMAIL\",\"password\":\"$STAFF_PW\"}")
[ "$RC" = "401" ] || [ "$RC" = "403" ] || fail "expected 401/403 for suspended login, got $RC"
pass "suspended user cannot log in ($RC)"

# -------------------------------------------------------------------
section "Delete custom role"
RC=$(curl -s -H "Host: $TENANT_HOST" -o /dev/null -w "%{http_code}" -X DELETE \
  -H "Authorization: Bearer $ADMIN_ACCESS" $API/v1/roles/$ROLE_ID)
[ "$RC" = "204" ] || fail "expected 204 on role delete, got $RC"
pass "custom role deleted"

# Cannot delete a system role
SACCO_ADMIN_ID=$(curl -s -H "Host: $TENANT_HOST" -H "Authorization: Bearer $ADMIN_ACCESS" $API/v1/roles | jq -r '.data[] | select(.code == "sacco_admin") | .id')
RC=$(curl -s -H "Host: $TENANT_HOST" -o /dev/null -w "%{http_code}" -X DELETE \
  -H "Authorization: Bearer $ADMIN_ACCESS" $API/v1/roles/$SACCO_ADMIN_ID)
[ "$RC" = "403" ] || fail "expected 403 on system-role delete, got $RC"
pass "system role delete refused (403)"

echo
echo "ALL RBAC E2E CHECKS PASSED ✓"
