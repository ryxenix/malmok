# Malmok (말목)

RKE2 클러스터를 **구축하고, 확장하고, 업그레이드하는** 단일 정적 바이너리.
TUI 마법사와 CLI를 같은 엔진 위에 얹었고, 런타임 의존성은 없습니다.

*[English README](README.md)*

> **상태: 알파.** 단일 노드와 2노드 구성은 실제 장비에서 반복 검증했고
> IDC 프로덕션 서버 구축에 사용했습니다. **3서버 HA·에어갭·외부
> 레지스트리는 아직 미검증입니다.** 아래 [검증 범위](#검증-범위)를
> 읽고 판단하십시오.

```
malmok apply --tui     # TUI 마법사 (구축·확장·재개·업그레이드)
malmok preflight       # 읽기 전용 사전 점검
malmok plan            # 무엇이 설치될지 계산 (네트워크 접근 없음)
malmok apply           # cluster.yaml 로 구축
malmok upgrade         # RKE2 버전 업그레이드 (노드 1대씩)
malmok report          # 감사 리포트 · DNS 레코드 시트
```

## 무엇을 하는 도구인가

노드 몇 대의 SSH 접근 권한과 원하는 형태를 적은 `cluster.yaml` 하나로,
동작하는 RKE2 클러스터를 만듭니다. 커널 파라미터·swap·방화벽 같은 준비부터
RKE2 부트스트랩, Cilium 데이터플레인, Gateway API, cert-manager, ArgoCD까지
한 흐름으로 처리합니다.

설계상 세 가지를 지킵니다:

- **관측은 추론이 아니라 실물입니다.** "포트가 열려 있을 것"이 아니라 실제로
  리스너를 띄워 상대 노드에서 닿는지 잽니다.
- **모든 단계는 멱등이고 재개 가능합니다.** 중간에 죽어도 처음부터 다시 돌지
  않습니다.
- **엔진은 화면을 모릅니다.** JSONL 이벤트만 방출하고, TUI는 그것을 그립니다
  (ADR-002). 터미널이 필요한 코드 경로는 결함으로 취급합니다.

## 설치

```bash
go install github.com/ryxen/malmok/cmd/malmok@latest
```

또는 [릴리스](https://github.com/ryxen/malmok/releases)에서 바이너리를
내려받으십시오 — linux·darwin, amd64·arm64, `SHA256SUMS` 동봉. 소스에서 빌드하려면:

```bash
git clone https://github.com/ryxen/malmok
cd malmok
go build -o bin/malmok ./cmd/malmok
```

Go 1.25 이상. 빌드 산출물은 단일 정적 바이너리라, 대상 노드에는 아무것도
설치하지 않아도 됩니다(SSH만 필요).

## 5분 사용법

```bash
# 1. 문서 검증 — 노드에 접속하지 않습니다
malmok plan -f cluster.yaml --validate-only

# 2. 사전 점검 — 노드를 재지만 아무것도 바꾸지 않습니다
malmok preflight -f cluster.yaml

# 3. 구축
malmok apply -f cluster.yaml

# 4. 결과 확인 (가장 최근 실행)
malmok report
```

설치 없이 화면만 보려면 `malmok apply --demo` — 노드에 접속하지 않고
전 과정을 흉내 냅니다.

TUI로 하려면 `malmok apply --tui`를 실행하십시오. 마법사가 `cluster.yaml`을
만들어 주고, 같은 화면에서 설치까지 진행합니다. 화면 언어는 영어가 기본이고
`--lang ko` 또는 설정 화면에서 한국어로 바꿀 수 있습니다(선택은 저장됩니다).

최소 `cluster.yaml`:

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
  # 재조인이 필요하다는 사실을 문서에 명시적으로 남깁니다 (ADR-008).
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
출력합니다.

더 많은 예시는 [`examples/`](examples/)에 있습니다.

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
| ACME(Let's Encrypt) 인증서 | 미검증 | 공인 DNS·계정 필요 |
| 3서버 HA (etcd 쿼럼) | **미검증** | 장비 미확보 |
| 에어갭 / 프록시 환경 | **미검증** | 스키마만 존재 |
| 외부 레지스트리 미러 | **미검증** | 스키마만 존재 |
| 스토리지 백엔드 | **범위 밖** | 앱 책임 |
| 관측(observability) 스택 | **미구현** | 스키마만 존재 |

검증 매트릭스는 7개 축의 조합을 정의하고, 커버리지를 **테스트로 강제**합니다
— 새 값을 추가하고 어느 조합에서도 쓰지 않으면 오프라인 테스트가 실패합니다.
설계와 실행 방법은 [`docs/40-verification-matrix.md`](docs/40-verification-matrix.md).

## 하지 않는 일

| 안 함 | 이유 |
|---|---|
| HTTPRoute 생성 | 앱 차트 책임입니다 (ADR-006). 게이트웨이까지가 이 도구의 몫 |
| ingress-nginx 설치 | 2026-03 EOL (ADR-005) |
| 사전 점검 단계에서 시스템 변경 | preflight는 읽기 전용입니다 |
| 전 노드 동시 재시작 | 1대씩, Ready 확인 후 진행 |
| `cluster.yaml`에 평문 시크릿 저장 | `SourceRef` 간접 참조만 받습니다 |

## 문서

| 문서 | 내용 |
|---|---|
| [`docs/00-architecture.md`](docs/00-architecture.md) | ADR 13건, 레이어 분해, 로드맵 |
| [`docs/10-preflight-plan.md`](docs/10-preflight-plan.md) | 프로브 카탈로그, 강등 결정 트리 |
| [`docs/11-execute.md`](docs/11-execute.md) | phase 계약, 멱등성, 재개, 이벤트 스키마 |
| [`docs/20-cert.md`](docs/20-cert.md) | 인증서 조립·검증·갱신 |
| [`docs/30-maintenance.md`](docs/30-maintenance.md) | 정기 점검, 보고서 |
| [`docs/40-verification-matrix.md`](docs/40-verification-matrix.md) | 검증 매트릭스 |
| [`docs/99-codes.md`](docs/99-codes.md) | 진단 코드 레지스트리 (생성물) |

문서는 한국어로 작성돼 있습니다. 로그·이벤트·에러 코드는 영어 고정입니다
— 한글 로그는 grep과 이슈 검색을 깨뜨리기 때문입니다.

## 진단 코드

실패는 전부 코드를 답니다. `PF-105`(swap 활성), `PF-601`(포트 도달 불가),
`EX-*`(실행 실패), `UP-*`(업그레이드 사전 조건) 같은 식입니다. 코드의 원천은
문서가 아니라 `internal/codes/`이고, `docs/99-codes.md`는 거기서 생성됩니다.
현재 145개가 등록돼 있습니다. 폐기된 번호는 재사용하지 않습니다 — 감사
리포트와 티켓이 릴리스보다 오래 남습니다.

## 기여

[CONTRIBUTING.md](CONTRIBUTING.md)를 보십시오. 이 저장소는 아직 단일 저자가
빠르게 바꾸고 있으니 PR 전에 이슈로 논의해 주십시오.

취약점은 이슈가 아니라 [SECURITY.md](SECURITY.md)의 절차로 보고해 주십시오
— 이 도구는 SSH 자격증명과 kubeconfig를 다룹니다.

## 라이선스

[Apache License 2.0](LICENSE).
