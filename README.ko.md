# Malmok (말목)

<p align="center"><img src="docs/img/malmok-wordmark.png" alt="말목 — 단단히 고정된 클러스터" width="620"></p>

[![ci](https://github.com/ryxenix/malmok/actions/workflows/ci.yml/badge.svg)](https://github.com/ryxenix/malmok/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/ryxenix/malmok?sort=semver)](https://github.com/ryxenix/malmok/releases)
[![go](https://img.shields.io/github/go-mod/go-version/ryxenix/malmok)](go.mod)
[![licence](https://img.shields.io/badge/licence-Apache--2.0-blue)](LICENSE)

**RKE2 클러스터를 구축·확장·업그레이드하는 단일 정적 바이너리.** TUI 마법사와
CLI가 같은 엔진 위에 있습니다. 대상 노드에는 에이전트나 런타임을 미리 설치할
필요가 없으며, Malmok이 SSH로 접속해 직접 준비합니다.

*[English README](README.md)*

## 이름의 뜻

`말목`은 경계를 표시하거나 지반을 보강하기 위해 땅에 단단히 박는 나무 말뚝을
뜻하는 한국어입니다. Malmok은 이를 로마자로 옮긴 이름입니다. 클러스터의 기반을
세우고, 첫 서버를 기준점으로 고정한 뒤 나머지 노드를 하나의 구조로 연결한다는
의미를 담았습니다.

<p align="center"><img src="docs/img/tui-wizard-ko.gif" alt="말목 마법사 처음부터 끝까지 - 설치 위치, 노드, 사전 검사, 설치, 결과" width="900"></p>

<sub>`malmok apply --tui --lang ko` 를 `--demo` 로 녹화한 것. 노드를 건드리지 않으므로 설치가
몇 초에 끝난다. 같은 실행을 화면 없이 하려면 `malmok apply -f cluster.yaml`.</sub>

> **상태: 알파.** 단일 노드와 2노드 구성은 실제 장비에서 반복 검증했고
> IDC 프로덕션 서버 구축에 사용했습니다. **3서버 HA·외부
> 레지스트리는 아직 미검증입니다.** 아래 [검증 범위](#검증-범위)를 먼저
> 읽으십시오.

```bash
malmok apply --tui     # TUI 마법사 (구축·확장·재개·업그레이드)
malmok plan -f cluster.yaml --validate-only  # 문서만 검증 (네트워크 접근 없음)
malmok preflight -f cluster.yaml             # 노드 읽기 전용 점검
malmok plan -f cluster.yaml                  # 노드를 측정하고 설치 계획 출력
malmok apply -f cluster.yaml                 # 문서에 따라 구축
TARGET_RKE2=v1.36.3+rke2r1                   # 목표 버전 지정
malmok upgrade --to "$TARGET_RKE2"            # RKE2 업그레이드 (노드 1대씩)
malmok report          # 감사 리포트 · DNS 레코드 시트
```

## 무엇을 하는 도구인가

노드 몇 대의 SSH 접근 권한과 원하는 형태를 적은 `cluster.yaml` 하나로,
동작하는 RKE2 클러스터를 만듭니다. 커널 파라미터·swap·방화벽 같은 준비부터
RKE2 부트스트랩, Cilium 데이터플레인, Gateway API, cert-manager, ArgoCD, 그리고
클러스터를 수집하는 VictoriaMetrics 관측 스택까지 한 흐름으로 처리합니다.
최초 서버에는 `kubectl`·`helm`·`k9s`가 클러스터 운영 계정의 PATH에 설치됩니다.
RKE2는 kubectl을 아무도 찾지 않는 곳에 묻어 두고 helm CLI는 아예 넣지 않아서,
예전에는 설치가 끝나도 접근할 수 없는 클러스터의 자격증명만 받는
셈이었습니다.

설계상 세 가지를 지킵니다:

- **관측은 추론이 아니라 실물입니다.** "포트가 열려 있을 것"이 아니라 실제로
  리스너를 띄워 상대 노드에서 닿는지 잽니다.
- **모든 단계는 멱등이고 재개 가능합니다.** 중간에 죽어도 처음부터 다시 돌지
  않습니다.
- **엔진은 화면을 모릅니다.** JSONL 이벤트만 방출하고, TUI는 그것을 그립니다
  ([ADR-002](docs/00-architecture.md#adr-002--tui는-엔진을-호출하고-렌더링한다-로직을-소유하지-않는다)).
  터미널이 필요한 코드 경로는 결함으로 취급합니다.

## 설치

[릴리스](https://github.com/ryxenix/malmok/releases)에서 플랫폼에 맞는 바이너리를
내려받고, 동봉된 `SHA256SUMS`와 대조하십시오. Linux·macOS, amd64·arm64 빌드를
제공합니다.

바이너리를 내려받은 뒤 `MALMOK_BIN`에 파일명을 지정하고 PATH에 설치하십시오.
Linux amd64의 예시는 다음과 같습니다.

```bash
MALMOK_BIN=./malmok_vX.Y.Z_linux_amd64  # X.Y.Z를 릴리스 버전으로 교체
chmod +x "$MALMOK_BIN"
sudo install "$MALMOK_BIN" /usr/local/bin/malmok
malmok --version
```

소스에서 설치하려면:

```bash
git clone https://github.com/ryxenix/malmok
cd malmok
go build -o bin/malmok ./cmd/malmok
```

Go 1.25.8 이상이 필요합니다. 다음 방법도 사용할 수 있습니다.

```bash
go install github.com/ryxenix/malmok/cmd/malmok@latest
```

단, `go install`로 만든 바이너리에는 릴리스 버전이 주입되지 않아 `malmok
--version`이 `dev`로 표시됩니다. 정확한 버전 식별이 중요할 때는 릴리스 바이너리를
사용하십시오.

## 요구 사항

- Malmok 실행 환경은 Linux 또는 macOS, amd64 또는 arm64여야 합니다.
- 대상 노드는 amd64 또는 arm64 기반 Ubuntu나 Rocky/RHEL 계열 Linux여야 합니다.
  현재 검증 프로파일은 Ubuntu 22.04/24.04와 Rocky 9를 대상으로 합니다. 그 밖의
  조합을 사용하기 전에 [검증 범위](#검증-범위)를 확인하십시오.
- 모든 대상 노드에 SSH로 접근할 수 있어야 하며, root 권한 또는 정상적인 권한
  상승 수단이 필요합니다. 로컬 단일 노드 설치는 SSH 없이 root로 실행할 수
  있습니다.
- 노드당 최소 20 GB 여유 디스크가 필요하며, 2 CPU 코어, 4 GB 메모리와 50 GB
  여유 디스크를 권장합니다. 정확한 차단 조건과 권장 사항은 preflight가
  보고합니다.

## 5분 사용법

설치 없이 화면만 보려면 `malmok apply --demo`를 실행하십시오. 노드에 접속하지
않고 주요 설치 흐름과 TUI 상태를 시뮬레이션합니다.

TUI로 하려면 `malmok apply --tui`를 실행하십시오. 마법사가 `cluster.yaml`을
만들어 주고, 같은 화면에서 설치까지 진행합니다. 화면 언어는 영어가 기본이고
`--lang ko` 또는 설정 화면에서 한국어로 바꿀 수 있습니다(선택은 저장됩니다).

아래 문서를 `cluster.yaml`로 저장하십시오. 예시 IP는 문서 전용 주소이므로 실제
환경에 맞는 주소, SSH 사용자, 키 경로와 RKE2 버전으로 바꿔야 합니다.

```yaml
apiVersion: platform.ryxen.dev/v1alpha1
kind: ClusterSpec

metadata:
  name: my-cluster
  profile: homelab        # 나머지 값은 프로파일이 채웁니다

network:
  mode: online

topology:
  # 서버가 한 대라 VIP가 없습니다. 나중에 서버를 늘리려면 전 노드
  # 재조인이 필요합니다 (docs/00-architecture.md의 ADR-008 참고).
  registrationAddress: 192.0.2.10
  acceptNodeRegistration: true
  servers:
    - host: 192.0.2.10
      ssh:
        user: ubuntu
        privateKey: file://~/.ssh/id_ed25519

kubernetes:
  version: v1.36.3+rke2r1
```

이 문서만으로 Cilium 데이터플레인·Gateway API·local-path 스토리지까지
프로파일이 채웁니다. 무엇이 채워졌는지는 `malmok plan`이 출처와 함께
출력합니다. 단일 서버의 등록 주소가 갖는 제약은
[ADR-008](docs/00-architecture.md#adr-008--ha는-2차지만-1차에서-경로를-예약한다)에
설명되어 있습니다.

더 많은 예시는 [`examples/`](examples/)에 있습니다.

이제 문서를 검증하고, 노드를 측정하고, 구축한 뒤 결과를 확인합니다.

```bash
# 1. 문서 검증 — 노드에 접속하지 않습니다
malmok plan -f cluster.yaml --validate-only

# 2. 사전 점검 — 노드를 측정하지만 아무것도 바꾸지 않습니다
malmok preflight -f cluster.yaml

# 3. 노드를 측정하고 확정된 설치 계획 확인
malmok plan -f cluster.yaml

# 4. 구축
malmok apply -f cluster.yaml

# 5. 결과 확인 (가장 최근 실행)
malmok report
```

## 검증 범위

인프라 도구가 과장하면 남의 클러스터가 깨집니다. 그래서 검증된 것과
안 된 것을 나눠 적습니다.

| 구성 | 상태 | 근거 |
|---|---|---|
| 단일 노드 (control-plane + etcd) | **실측 검증** | IDC 프로덕션 구축 완주 |
| 2노드 (server + agent) | **실측 검증** | 랩 하네스 반복 실행 |
| 노드 추가(grow) / 중단 후 재개 / 재적용 | **실측 검증** | 검증 매트릭스 |
| RKE2 업그레이드 | **실측 검증** | 검증 매트릭스 |
| Cilium 데이터플레인 + Gateway API | **실측 검증** | 외부 IP 도달 확인 |
| private-CA 리스너 인증서 | **실측 검증** | 랩 하네스 |
| ACME HTTP-01 인증서 | **실측 검증** | 프로덕션 클러스터에서 Let's Encrypt 발급, TLS 1.3·체인·호스트명 확인 |
| ACME DNS-01 인증서(와일드카드) | 미검증 | DNS 존 자격증명 필요 |
| 3서버 HA (etcd 쿼럼) | **미검증** | 장비 미확보 |
| 에어갭 설치 (외부 통신 차단) | **하드웨어 검증** | 노드 2대 egress 차단, RKE2·Cilium·Gateway API 모두 반입 아티팩트로 |
| 프록시 환경 | **미검증** | 스키마만 존재 |
| 외부 레지스트리 미러 | **미검증** | 스키마만 존재 |
| 스토리지 백엔드 | **범위 밖** | 앱 책임 |
| 관측(VictoriaMetrics) | **실측 검증** | 프로덕션 클러스터에 설치, 19개 대상 수집·저장 확인 |

검증 매트릭스는 7개 축의 조합을 정의하고, 커버리지를 **테스트로 강제**합니다
— 새 값을 추가하고 어느 조합에서도 쓰지 않으면 오프라인 테스트가 실패합니다.
설계와 실행 방법은 [`docs/40-verification-matrix.md`](docs/40-verification-matrix.md).

## 하지 않는 일

| 안 함 | 이유 |
|---|---|
| HTTPRoute 생성 | 앱 차트 책임입니다 ([ADR-006](docs/00-architecture.md#adr-006--httproute는-애플리케이션-차트-책임)). 게이트웨이까지가 이 도구의 몫 |
| ingress-nginx 설치 | 2026-03 EOL ([ADR-005](docs/00-architecture.md#adr-005--ingress-nginx를-사용하지-않는다)) |
| 사전 점검 단계에서 시스템 변경 | preflight는 읽기 전용입니다 |
| 전 노드 동시 재시작 | 1대씩, Ready 확인 후 진행 |
| `cluster.yaml`에 평문 시크릿 저장 | `SourceRef` 간접 참조만 받습니다 |

## 상세 문서

- [아키텍처와 설계 결정](docs/00-architecture.md)
- [사전 점검과 계획](docs/10-preflight-plan.md) · [실행·재개·이벤트](docs/11-execute.md)
- [인증서](docs/20-cert.md) · [Day-2 유지보수](docs/30-maintenance.md)
- [검증 매트릭스](docs/40-verification-matrix.md) · [진단 코드 레지스트리](docs/99-codes.md)

## 진단 코드

운영 점검과 실행 실패에는 안정적인 코드를 붙입니다. `PF-105`(swap 활성),
`PF-601`(포트 도달 불가), `EX-*`(실행 실패), `UP-*`(업그레이드 사전 조건) 같은
식입니다. 코드의 원천은 문서가 아니라 `internal/codes/`이고,
[`docs/99-codes.md`](docs/99-codes.md)는 거기서 생성됩니다. 폐기된 번호는
재사용하지 않습니다 — 감사 리포트와 티켓이 릴리스보다 오래 남습니다.

## 기여

[CONTRIBUTING.md](CONTRIBUTING.md)를 보십시오. 이 저장소는 아직 단일 저자가
빠르게 바꾸고 있으니 PR 전에 이슈로 논의해 주십시오.

취약점은 이슈가 아니라 [SECURITY.md](SECURITY.md)의 절차로 보고해 주십시오
— 이 도구는 SSH 자격증명과 kubeconfig를 다룹니다.

## 라이선스

[Apache License 2.0](LICENSE).
