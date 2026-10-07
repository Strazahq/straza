@echo off
rem SSH tunnels to a remote Straza eval stack
rem (https://docs.straza.ai/get-started/enterprise-demo-stack/#demo-defaults-and-the-hardened-shape).
rem Uses the OpenSSH client that ships with Windows 10+.
rem Usage:  tunnels.bat user@host
rem Then open http://localhost:8400 (Ctrl+C here stops the tunnels). Use
rem localhost, not 127.0.0.1: Keycloak's sign-in cookie is bound to that name.
rem Ports: 8400 landing page, 8420 strazad + console, 8443 approver https
rem (phone), 8480 Keycloak, 8087 midPoint, 5601 Kibana (compose.siem.yaml
rem overlay), 8477 governed-agents demo chat face (compose.demo.yaml overlay,
rem which builds from a checkout of the straza-agents-demo repository, which is
rem not public).
if "%~1"=="" (
  echo usage: %~nx0 user@host
  exit /b 2
)
echo tunneling 8400/8420/8443/8480/8087/5601/8477 -^> %~1  (Ctrl+C to stop)
ssh -N -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -L 8400:localhost:8400 -L 8420:localhost:8420 -L 8443:localhost:8443 -L 8480:localhost:8480 -L 8087:localhost:8087 -L 5601:localhost:5601 -L 8477:localhost:8477 %~1
