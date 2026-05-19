#!/usr/bin/env bash
# E2E test: forgot password -> reset -> login with new password ->
# verify old refresh token revoked -> change password -> verify revoked.
set -euo pipefail

API=http://localhost:8081
MAIL=http://localhost:8025
EMAIL="admin@nexussacco.local"
OLD_PW="${SEED_PW:-ChangeMeOnFirstLogin!1}"
NEW_PW="NewSecret-Reset-2026-A"
NEWER_PW="EvenNewer-Changed-2026-A"

pass() { printf "  [✓] %s\n" "$1"; }
fail() { printf "  [✗] %s\n" "$1"; exit 1; }
section() { printf "\n=== %s ===\n" "$1"; }

# -------------------------------------------------------------------
section "Setup: capture pre-reset refresh token"
# do_login EMAIL PASSWORD -> echoes "<access>|<refresh>"
do_login() {
  local e="$1" p="$2"
  local resp r mfa_token code body msgs
  resp=$(curl -s -X POST $API/v1/auth/login \
    -H "Content-Type: application/json" \
    -H "X-Tenant-Slug: platform" \
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
    resp=$(curl -s -X POST $API/v1/auth/mfa/verify \
      -H "Content-Type: application/json" \
      -H "X-Tenant-Slug: platform" \
      -d "{\"mfa_token\":\"$mfa_token\",\"code\":\"$code\"}")
  fi
  local at rt
  at=$(echo "$resp" | jq -r '.data.access_token // empty')
  rt=$(echo "$resp" | jq -r '.data.refresh_token // empty')
  [ -n "$at" ] || { echo "ERR:NO_TOKEN:$resp" >&2; return 1; }
  echo "$at|$rt"
}

curl -s -X DELETE $MAIL/api/v1/messages > /dev/null
PAIR=$(do_login "$EMAIL" "$OLD_PW") || fail "initial login: $PAIR"
ACCESS="${PAIR%%|*}"
REFRESH="${PAIR##*|}"
[ -n "$ACCESS" ] && [ "$ACCESS" != "null" ] || fail "no access token: $LOGIN"
pass "logged in with old password, got refresh token"
curl -s -X DELETE $MAIL/api/v1/messages > /dev/null

# -------------------------------------------------------------------
section "1. POST /v1/auth/password/forgot"
RESP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST $API/v1/auth/password/forgot \
  -H "Content-Type: application/json" \
  -H "X-Tenant-Slug: platform" \
  -d "{\"email\":\"$EMAIL\"}")
[ "$RESP_CODE" = "204" ] || fail "expected 204, got $RESP_CODE"
pass "forgot returned 204"

# Also check that nonexistent email returns 204 (no leak)
RESP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST $API/v1/auth/password/forgot \
  -H "Content-Type: application/json" \
  -H "X-Tenant-Slug: platform" \
  -d '{"email":"nobody-at-all@example.com"}')
[ "$RESP_CODE" = "204" ] || fail "expected 204 for unknown email, got $RESP_CODE"
pass "unknown email also returns 204 (no enumeration)"

# -------------------------------------------------------------------
section "2. Extract reset link from MailHog"
TOKEN=""
for i in 1 2 3 4 5 6 7 8; do
  sleep 1
  MSGS=$(curl -s "$MAIL/api/v2/messages")
  TOTAL=$(echo "$MSGS" | jq -r '.total')
  if [ "$TOTAL" -ge 1 ]; then
    # find the message addressed to our admin (skip emails for nobody@)
    IDX=$(echo "$MSGS" | jq -r --arg e "$EMAIL" \
      '.items | to_entries[] | select(.value.Content.Headers.To[0] | ascii_downcase == ($e | ascii_downcase)) | .key' | head -1)
    [ -z "$IDX" ] && continue
    BODY=$(echo "$MSGS" | jq -r ".items[$IDX].Content.Body" | tr -d '\r')
    # token is in a URL like ...?token=XXXX
    TOKEN=$(echo "$BODY" | grep -oE 'token=[A-Za-z0-9_-]+' | head -1 | cut -d= -f2 || true)
    [ -n "$TOKEN" ] && break
  fi
done
[ -n "$TOKEN" ] || fail "could not extract reset token from MailHog"
pass "extracted token: ${TOKEN:0:16}...(${#TOKEN} chars)"

# -------------------------------------------------------------------
section "3. POST /v1/auth/password/reset"
RESP=$(curl -s -w "\n%{http_code}" -X POST $API/v1/auth/password/reset \
  -H "Content-Type: application/json" \
  -H "X-Tenant-Slug: platform" \
  -d "{\"token\":\"$TOKEN\",\"new_password\":\"$NEW_PW\"}")
CODE=$(echo "$RESP" | tail -1)
BODY=$(echo "$RESP" | sed '$d')
[ "$CODE" = "204" ] || [ "$CODE" = "200" ] || fail "expected 200/204, got $CODE / $BODY"
pass "reset succeeded (204)"

# Replay the token — should fail
RESP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST $API/v1/auth/password/reset \
  -H "Content-Type: application/json" \
  -H "X-Tenant-Slug: platform" \
  -d "{\"token\":\"$TOKEN\",\"new_password\":\"WhateverElse-2026\"}")
[ "$RESP_CODE" = "400" ] || [ "$RESP_CODE" = "401" ] || fail "expected 400/401 on replay, got $RESP_CODE"
pass "replayed token rejected ($RESP_CODE)"

# -------------------------------------------------------------------
section "4. Old password no longer works"
RESP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST $API/v1/auth/login \
  -H "Content-Type: application/json" \
  -H "X-Tenant-Slug: platform" \
  -d "{\"email\":\"$EMAIL\",\"password\":\"$OLD_PW\"}")
[ "$RESP_CODE" = "401" ] || fail "expected 401 with old password, got $RESP_CODE"
pass "old password rejected (401)"

# -------------------------------------------------------------------
section "5. Login with new password"
curl -s -X DELETE $MAIL/api/v1/messages > /dev/null
PAIR=$(do_login "$EMAIL" "$NEW_PW") || fail "login with new pw: $PAIR"
NEW_ACCESS="${PAIR%%|*}"
NEW_REFRESH="${PAIR##*|}"
pass "logged in with new password"

# -------------------------------------------------------------------
section "6. Pre-reset refresh token revoked"
RESP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST $API/v1/auth/refresh \
  -H "Content-Type: application/json" \
  -d "{\"refresh_token\":\"$REFRESH\"}")
[ "$RESP_CODE" = "401" ] || fail "expected 401 for pre-reset refresh, got $RESP_CODE"
pass "pre-reset refresh token rejected (401)"

# -------------------------------------------------------------------
section "7. Change password while authenticated"
RESP=$(curl -s -w "\n%{http_code}" -X POST $API/v1/auth/password/change \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $NEW_ACCESS" \
  -d "{\"current_password\":\"$NEW_PW\",\"new_password\":\"$NEWER_PW\"}")
CODE=$(echo "$RESP" | tail -1)
BODY=$(echo "$RESP" | sed '$d')
[ "$CODE" = "204" ] || [ "$CODE" = "200" ] || fail "expected 200/204, got $CODE / $BODY"
pass "change-password succeeded (204)"

# Wrong current password → 401
RESP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST $API/v1/auth/password/change \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $NEW_ACCESS" \
  -d "{\"current_password\":\"wrongwrongwrong-xxx\",\"new_password\":\"FooBar-12345\"}")
[ "$RESP_CODE" = "401" ] || fail "expected 401 with wrong current pw, got $RESP_CODE"
pass "wrong current password rejected (401)"

# Refresh tokens issued before change-password should now be revoked
RESP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST $API/v1/auth/refresh \
  -H "Content-Type: application/json" \
  -d "{\"refresh_token\":\"$NEW_REFRESH\"}")
[ "$RESP_CODE" = "401" ] || fail "expected 401 for post-change refresh, got $RESP_CODE"
pass "post-change refresh token rejected (401)"

# -------------------------------------------------------------------
section "8. Login with the newer password"
curl -s -X DELETE $MAIL/api/v1/messages > /dev/null
PAIR=$(do_login "$EMAIL" "$NEWER_PW") || fail "login with newer pw: $PAIR"
pass "logged in with newer password"

# -------------------------------------------------------------------
section "9. Reset the platform admin back to seed password"
# So future runs / manual use still work with the documented credential.
curl -s -X DELETE $MAIL/api/v1/messages > /dev/null
curl -s -X POST $API/v1/auth/password/forgot \
  -H "Content-Type: application/json" \
  -H "X-Tenant-Slug: platform" \
  -d "{\"email\":\"$EMAIL\"}" > /dev/null
TOKEN=""
for i in 1 2 3 4 5 6 7 8; do
  sleep 1
  MSGS=$(curl -s "$MAIL/api/v2/messages")
  TOTAL=$(echo "$MSGS" | jq -r '.total')
  if [ "$TOTAL" -ge 1 ]; then
    IDX=$(echo "$MSGS" | jq -r --arg e "$EMAIL" \
      '.items | to_entries[] | select(.value.Content.Headers.To[0] | ascii_downcase == ($e | ascii_downcase)) | .key' | head -1)
    [ -z "$IDX" ] && continue
    BODY=$(echo "$MSGS" | jq -r ".items[$IDX].Content.Body" | tr -d '\r')
    TOKEN=$(echo "$BODY" | grep -oE 'token=[A-Za-z0-9_-]+' | head -1 | cut -d= -f2 || true)
    [ -n "$TOKEN" ] && break
  fi
done
[ -n "$TOKEN" ] || fail "could not get reset token to restore"
curl -s -X POST $API/v1/auth/password/reset \
  -H "Content-Type: application/json" \
  -H "X-Tenant-Slug: platform" \
  -d "{\"token\":\"$TOKEN\",\"new_password\":\"$OLD_PW\"}" > /dev/null
pass "platform admin password restored to seed value"

echo
echo "ALL PASSWORD-RESET E2E CHECKS PASSED ✓"
