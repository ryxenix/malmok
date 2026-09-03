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

## 누구를 위한 도구인가

Malmok은 베어메탈·VM·온프레미스·DMZ·에어갭처럼 직접 관리하는 환경에 RKE2
클러스터를 구축하는 플랫폼·SRE·인프라 엔지니어를 위한 도구입니다.

`cluster.yaml`과 SSH 접근 권한만으로 반복 가능하고, 중단 후 재개할 수 있으며,
감사 가능한 클러스터 구축 절차를 제공합니다. 설치 전에 실제 환경을 측정하고,
장애가 나면 이어서 실행하며, 구축 결과를 명확한 기록과 함께 인수인계해야 하는
환경에 특히 적합합니다.

Malmok은 관리형 Kubernetes 서비스나 애플리케이션 배포 플랫폼이 아닙니다.
클러스터 기반까지 구축하고 운영하며, 그 위의 애플리케이션은 각 팀이 소유합니다.

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
> 읽으십시오. 문서 스키마는 `v1alpha1`이며 마이너 릴리스 사이에 바뀔 수
> 있습니다. `malmok plan --validate-only`가 인식하지 못하는 필드를 모두
> 짚어 줍니다.

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
동작하는 RKE2 클러스터를 만듭니다. 커널 파라미터·swap·데이터 디렉터리 같은 준비부터
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
- 노드끼리 6443(쿠버네티스 API), 9345(RKE2 supervisor — 쿠버네티스 포트
  문서 어디에도 없어서 가장 많이 놓칩니다), 2379-2380(etcd, 서버 간),
  10250(kubelet)과 데이터플레인 자체 포트에 도달할 수 있어야 합니다.
  preflight는 이를 추정하지 않습니다. 한쪽 노드에 실제 리스너를 띄우고
  상대 노드에서 접속해 봅니다.

## 노드에서 무엇을 바꾸는가

말목이 root를 요구하는 이유는 호스트를 준비하기 때문입니다. 권한을 주기 전에
무엇을 하는지 알 수 있어야 하므로, 소스가 아니라 여기에 전부 적습니다.

- `br_netfilter`·`overlay`를 적재하고 `/etc/modules-load.d/90-malmok.conf`를
  써서 재부팅 후에도 유지되게 합니다.
- `/etc/sysctl.d/90-malmok.conf`를 씁니다. IPv4·IPv6 포워딩, 양쪽 계열의
  bridge netfilter, inotify 한도 상향.
- swap을 끄고 `/etc/fstab`에서 제거합니다. 둘 다 필요합니다. kubelet은 swap이
  켜져 있으면 기동을 거부하고, fstab에 남은 항목은 다음 부팅에 swap을 되살립니다.
- RKE2가 자라날 디렉터리 `/var/lib/rancher`를 만듭니다.
- 릴리스 tarball로 RKE2를 설치하고 systemd 유닛을 관리합니다.
- 클러스터 매니페스트를 `/var/lib/rancher/rke2/server/manifests`에 씁니다.
- 최초 서버에 한해 `kubectl`·`helm`·`k9s`를 운영 계정 PATH에 놓습니다.
- 문서가 요구할 때만: 사설 CA를 노드 신뢰 저장소에 설치하고, containerd
  레지스트리 설정을 씁니다(레지스트리 비밀번호를 담을 수 있어 0600).

방화벽은 **건드리지 않습니다.** preflight가 활성 방화벽과 열려야 할 포트를
보고할 뿐, 여는 것은 정책을 소유한 쪽의 몫입니다. 노드를 재부팅하지도
않습니다. 업그레이드는 노드를 1대씩, 각각 Ready를 확인하며 서비스를
재시작합니다.

## 5분 사용법

설치 없이 화면만 보려면 `malmok apply --demo`를 실행하십시오. 노드에 접속하지
않고 주요 설치 흐름과 TUI 상태를 시뮬레이션합니다.

TUI로 하려면 `malmok apply --tui`를 실행하십시오. 마법사가 `cluster.yaml`을
만들어 주고, 같은 화면에서 설치까지 진행합니다. 화면 언어는 영어가 기본이고
`--lang ko` 또는 설정 화면에서 한국어로 바꿀 수 있습니다(선택은 저장됩니다).

아래 문서를 `cluster.yaml`로 저장하십시오. 예시 IP는 문서 전용 주소이므로 실제
환경에 맞는 주소, SSH 사용자, 키 경로와 RKE2 버전으로 바꿔야 합니다.

> RKE1의 `cluster.yml`이 아닙니다. 두 형식은 서로 무관합니다. `rke up`은 이
> 파일을 읽지 못하고, 말목도 그 파일을 읽지 못합니다.

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
  # 재조인이 필요하므로 먼저 안정적인 등록 주소를 마련해야 합니다.
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
출력합니다. 단일 서버 주소는 안정적인 등록 엔드포인트가 아닙니다. 서버를
추가하기 전에 안정적인 DNS 이름이나 가상 IP를 도입해야 하며, 등록 주소가
바뀌면 노드를 다시 조인해야 합니다.

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
| HTTPRoute 생성 | 앱 차트 책임입니다. 게이트웨이까지가 이 도구의 몫입니다 |
| ingress-nginx 설치 | 2026-03 EOL이며, Malmok은 대신 Gateway API를 설치합니다 |
| 사전 점검 단계에서 시스템 변경 | preflight는 읽기 전용입니다 |
| 전 노드 동시 재시작 | 1대씩, Ready 확인 후 진행 |
| `cluster.yaml`에 평문 시크릿 저장 | `SourceRef` 간접 참조만 받습니다 |
| 머신 프로비저닝 | 말목은 이미 SSH에 응답하는 호스트에서 시작합니다. VM·네트워크·DNS 레코드는 OpenTofu, Proxmox 또는 해당 사이트 도구의 몫 |
| 애플리케이션 배포 | ArgoCD를 설치하고 저장소를 가리키게 할 뿐, 무엇을 동기화할지는 사용자 몫 |
| 노드 방화벽 변경 | preflight가 활성 방화벽과 필요한 포트를 보고합니다. 정책은 그것을 소유한 쪽의 몫 |
| 구축 이후 클러스터 운영 | 메트릭 스택을 설치하고 kubeconfig를 넘겨줍니다. 알림·대시보드·day-2는 이 도구가 아닙니다 |

## 상세 문서

현재 공개 문서는 지원 범위의 근거와 실행 문제를 진단하는 코드 레지스트리를
포함합니다. 설치·설정·운영 가이드는 프로젝트 문서 사이트와 함께 순차적으로
공개합니다.

- [검증 매트릭스](docs/40-verification-matrix.md)
- [진단 코드 레지스트리](docs/99-codes.md)

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
