<h1 align="center">Malmok (말목)</h1>

<p align="center"><img src="docs/img/malmok-wordmark.png" alt="말목 — 단단히 고정된 클러스터" width="620"></p>

<p align="center">
  <a href="https://github.com/ryxenix/malmok/actions/workflows/ci.yml"><img src="https://img.shields.io/badge/CI-GitHub_Actions-2088FF?logo=githubactions&amp;logoColor=white" alt="CI: GitHub Actions"></a>
  <img src="https://img.shields.io/badge/status-alpha-f59e0b" alt="상태: 알파">
  <img src="https://img.shields.io/badge/Go-1.25.8-00ADD8?logo=go&amp;logoColor=white" alt="Go 1.25.8">
  <a href="LICENSE"><img src="https://img.shields.io/badge/licence-Apache--2.0-blue" alt="Apache-2.0 라이선스"></a>
</p>

<p align="center"><strong>직접 관리하는 인프라에 RKE2 클러스터를 구축합니다 — 에이전트 없이, 반복 가능하게, 중단돼도 이어서.</strong></p>

<p align="center">말목은 경계를 표시하거나 지반을 보강하기 위해 땅에 박는 나무 말뚝을 뜻하는 한국어입니다. 클러스터의 기반을 단단히 세운다는 의미를 담았습니다.</p>

<p align="center"><sub>베어메탈 · VM · 온프레미스 · DMZ · 에어갭(<a href="#검증-범위">검증됨</a>)</sub></p>

<p align="center">
  <a href="#5분-사용법"><strong>시작하기</strong></a> ·
  <a href="#검증-범위"><strong>검증 범위</strong></a> ·
  <a href="https://malmok.dev/ko/"><strong>문서</strong></a> ·
  <a href="https://github.com/ryxenix/malmok/releases"><strong>릴리스</strong></a>
</p>

<p align="center"><em><a href="README.md">English</a></em></p>

<p align="center"><img src="docs/img/tui-wizard-ko.gif" alt="말목 마법사 처음부터 끝까지 - 설치 위치, 노드, 사전 검사, 설치, 결과" width="900"></p>

<p align="center"><sub>데모 모드의 <code>malmok apply --tui --lang ko</code> — 같은 엔진을 화면 없이 실행하려면 <code>malmok apply -f cluster.yaml</code>.</sub></p>

| 먼저 측정 | 안전하게 재개 | 전 과정을 기록 |
|:---:|:---:|:---:|
| 변경 전에 실제 노드와 네트워크를 점검 | 중단된 실행을 처음부터가 아니라 이어서 진행 | 안정적인 진단 코드·JSONL 이벤트·인수인계 리포트 보존 |

> **상태: 알파.** 기존 Linux 서버에 RKE2를 반복 구축하는 알파 단계 도구입니다.
> 홈랩과 평가 환경에서 사용해 보고 피드백을 주십시오. 업무 환경 도입 전에는
> [검증 범위](#검증-범위)와 제한을 확인하십시오. 단일 노드와 2노드 구성은 실제
> 장비에서 업그레이드·재개·재적용까지 반복 검증했고, 서버 3대 구성은 구축과
> 서버 하나를 잃는 장애까지 검증했습니다. **프록시·외부 레지스트리는
> 미검증**입니다. 에어갭은 검증됐습니다 — egress를 끊은 상태에서
> RKE2·차트·전 이미지를 반입 파일로 설치합니다. 문서 스키마는 `v1alpha1`이며
> 마이너 릴리스 사이에 바뀔 수 있습니다.
>
> 버전은 호환성 약속이지 완성도 점수가 아닙니다. `0.x` 는 스키마가 아직 움직일
> 수 있다는 뜻이고, 0.95 라는 숫자 자체는 1.0 이 얼마나 가까운지 말하지
> 않습니다. **1.0 은 스키마가 `v1` 이 되고 [검증 범위](#검증-범위) 표에서
> *미검증* 인 행들이 검증될 때입니다.** 8월에 0.1.0 이었고, 그 사이에 무엇을
> 했는지는 CHANGELOG 에 있습니다.

## 누구를 위한 도구인가

Malmok은 이미 준비된 Linux 서버에 `cluster.yaml`과 SSH 접근 권한으로
RKE2 클러스터를 직접 구축하고 관리하려는 사용자를 위한 도구입니다.

### 개인 프로젝트·홈랩에서

집의 서버·미니 PC나 개인 VM에 RKE2를 직접 구축하고 운영하려는 사용자에게:

- 수동 설치 절차를 매번 찾아보지 않고 구성 파일로 클러스터를 다시 구축하고 싶을 때
- 노드를 실험하거나 홈랩을 재설치하면서 반복 작업을 줄이고 싶을 때
- 별도 관리 클러스터 없이 작업 PC의 SSH와 CLI로 시작하고 싶을 때

### 업무·팀 환경에서

고객사가 준비한 서버에 솔루션을 반복 납품하는 개발사·SI 팀·구축 엔지니어와
사내 인프라 담당자에게. **RKE2를 사용하기로 결정했지만 클러스터 기반은 아직
구축해야 하는 상황**을 대상으로 합니다.

- AI·문서처리·검색·데이터 플랫폼 등을 납품하며 고객사별 RKE2 설치를 공통 절차로 정리하고 싶을 때
- 변경 전에 실제 서버 상태와 구축을 막는 요인을 확인하고 싶을 때
- 중단된 구축을 재개하고 검토·인수인계를 위한 실행 기록을 남겨야 할 때
- 폐쇄망 설치가 필요해 [에어갭 지원 조건과 검증 범위](docs/guides/air-gap.ko.md)를 확인하고 준비하려 할 때

**어느 쪽이든 알파 소프트웨어입니다. 프로덕션 납품에 검증된 도구가 아니며,
스키마가 "지원"하는 것과 [아래 표](#검증-범위)가 "검증"한 것은 다릅니다. 행을
믿기 전에 표를 읽으십시오.**

Malmok은 VM 생성이나 범용 구성 관리, 애플리케이션 배포 도구를 대체하지 않습니다.
클러스터 기반까지 구축하고 운영하며, 머신과 애플리케이션은 사용자가 관리합니다.

### 서버가 준비된 다음, 클러스터를 인계하기까지

사내 인프라나 고객사 IDC에 서버가 준비되어 있어도 네트워크 확인, 호스트 설정,
설치 자재 준비와 결과 검증은 남습니다. 말목은 구성 파일과 실행 기록으로
현장마다 반복되는 구축 작업을 일관되게 수행하도록 돕습니다.

초기 버전은 개발자가 실제 IDC 서버에 클러스터를 구축하는 데 사용했습니다.
이 경험을 바탕으로 사전 점검·중단 후 재개·인수인계 기능을 발전시키고 있습니다.
이 경험이 모든 업무 환경의 안정성을 보증하지는 않습니다.

[온프레미스 구축 가이드: 준비·검증·인수인계 →](docs/guides/on-premises.ko.md)

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
  `registry.mode: embedded`이면 5001도 필요합니다. 노드가 보유 이미지를
  광고하는 통로인데, 막혀 있어도 실패하지 않고 업스트림 레지스트리로
  넘어갑니다 — 폐쇄망에서는 그게 멈추는 pull입니다. preflight는 이를
  추정하지 않습니다. 한쪽 노드에 실제 리스너를 띄우고 상대 노드에서
  접속해 봅니다.

## 설치

최신 릴리스를 명령 하나로 설치할 수 있습니다.

```bash
curl -fsSL https://malmok.dev/install.sh | sh
```

Bash와 Fish에서 같은 명령을 사용합니다. 내려받은 설치 스크립트 자체는 POSIX
`sh`에서 실행됩니다.

설치 스크립트는 Linux·macOS와 amd64·arm64를 판별하고, 알맞은
[릴리스](https://github.com/ryxenix/malmok/releases)를 내려받아 공개된
`SHA256SUMS`로 검증한 뒤 `/usr/local/bin`에 `malmok`을 설치합니다. 실행 전에
스크립트를 직접 확인하려면 다음과 같이 사용하십시오.

```bash
curl -fsSLo install-malmok.sh https://malmok.dev/install.sh
less install-malmok.sh
sh install-malmok.sh
```

특정 릴리스, 설치 경로 또는 Linux 에어갭 바이너리를 선택하려면 `sh -s --`
뒤에 `--version vX.Y.Z`, `--bin-dir ~/.local/bin`, `--airgap`을 전달합니다.

```bash
curl -fsSL https://malmok.dev/install.sh | sh -s -- --version v0.83.0
```

릴리스 페이지에서 바이너리를 직접 내려받을 수도 있습니다. Linux·macOS,
amd64·arm64 빌드를 제공하며, 동봉된 `SHA256SUMS`와 대조해야 합니다.

바이너리를 내려받은 뒤 `MALMOK_BIN`에 파일명을 지정하고 PATH에 설치하십시오.
Linux amd64의 예시는 다음과 같습니다.

```bash
# X.Y.Z를 릴리스 버전으로 교체
MALMOK_BIN=./malmok_vX.Y.Z_linux_amd64
chmod +x "$MALMOK_BIN"
sudo install "$MALMOK_BIN" /usr/local/bin/malmok
malmok --version
```

Fish에서 직접 설치할 때는 다음과 같습니다.

```fish
# X.Y.Z를 릴리스 버전으로 교체
set MALMOK_BIN ./malmok_vX.Y.Z_linux_amd64
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

## 마법사로 시작하기

`cluster.yaml`을 미리 작성할 필요가 없습니다. 말목을 설치한 뒤 마법사에
대상 노드 주소와 SSH 접근 정보를 입력하면, 구성 파일 생성부터 점검과 구축까지 안내합니다.

```bash
# 마법사에서 설정하고 클러스터 구축
malmok apply --tui --lang ko
```

수동 다운로드와 폐쇄망 환경은 [설치 옵션](#설치)을 확인하십시오.

## 구성 파일로 반복·자동화하기

마법사의 선행 작업이 아니라, 파일로 구성을 관리하려는 사용자를 위한 별도 경로입니다.

말목 클러스터를 구성하는 데 필요한 최소 형태입니다. 문서용 주소, SSH 계정,
키와 RKE2 버전을 실제 환경의 값으로 바꾼 뒤 `cluster.yaml`로 저장하십시오.

> RKE1의 `cluster.yml`이 아닙니다. 두 형식은 서로 무관합니다. `rke up`은 이
> 파일을 읽지 못하고, 말목도 그 파일을 읽지 못합니다.

```yaml
apiVersion: malmok.dev/v1alpha1
kind: ClusterSpec

metadata:
  name: my-cluster
  profile: homelab

network:
  mode: online

topology:
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

프로파일이 Cilium 데이터플레인·Gateway API·local-path 스토리지를 채우고,
`malmok plan`이 확정된 모든 값과 출처를 출력합니다. 단일 서버 주소는 안정적인
등록 엔드포인트가 아니므로 서버를 추가하기 전에 안정적인 DNS 이름이나 가상 IP를
도입해야 합니다. 다른 구성은 [`examples/`](examples/)에 있습니다.

## 하나의 엔진, 두 가지 사용 방식

안내가 필요할 때는 TUI로 실행하고, 같은 작업을 검토·반복·자동화해야 할 때는
CLI로 실행합니다.

```bash
# TUI 마법사: 구축·확장·재개·업그레이드
malmok apply --tui

# 네트워크 접근 없이 문서만 검증
malmok plan -f cluster.yaml --validate-only

# 노드 읽기 전용 점검
malmok preflight -f cluster.yaml

# 노드를 측정하고 설치 계획 출력
malmok plan -f cluster.yaml

# 문서에 따라 구축
malmok apply -f cluster.yaml

# 목표 버전을 지정하고 노드 한 대씩 RKE2 업그레이드
TARGET_RKE2=v1.36.3+rke2r1
malmok upgrade --to "$TARGET_RKE2"

# 감사 리포트와 DNS 레코드 시트 생성
malmok report

# 폐쇄망에 반입해야 하는 컨테이너 이미지 목록
malmok images -f cluster.yaml
```

<details>
<summary>Fish 셸</summary>

```fish
# TUI 마법사: 구축·확장·재개·업그레이드
malmok apply --tui

# 네트워크 접근 없이 문서만 검증
malmok plan -f cluster.yaml --validate-only

# 노드 읽기 전용 점검
malmok preflight -f cluster.yaml

# 노드를 측정하고 설치 계획 출력
malmok plan -f cluster.yaml

# 문서에 따라 구축
malmok apply -f cluster.yaml

# 목표 버전을 지정하고 노드 한 대씩 RKE2 업그레이드
set TARGET_RKE2 v1.36.3+rke2r1
malmok upgrade --to "$TARGET_RKE2"

# 감사 리포트와 DNS 레코드 시트 생성
malmok report

# 폐쇄망에 반입해야 하는 컨테이너 이미지 목록
malmok images -f cluster.yaml
```

</details>

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

## 자주 묻는 질문

### 고객사가 서버를 이미 준비했다면 무엇을 해주나요?

바로 그 시점부터 말목의 역할이 시작됩니다. 준비된 Linux 서버에 SSH로 접근해
사전 점검, RKE2 구축 계획 확인, 설치, 중단 후 재개와 결과 리포트 생성을
수행합니다. 서버를 어떤 도구로 준비했는지는 전제하지 않습니다.

### Terraform이나 Ansible로 구축하면 되지 않나요?

가능합니다. 이미 검증된 RKE2 자동화가 있다면 교체할 필요는 없습니다.
말목은 IaC라는 방식을 대체하는 도구가 아닙니다. 선언형 `cluster.yaml`을 사용하며,
RKE2에 특화된 점검·실행·복구·리포트 흐름을 직접 작성하고 유지하는 부담을 줄입니다.

### k0sctl이나 CAPRKE2와는 무엇이 다른가요?

k0sctl은 k0s를, 말목은 RKE2를 대상으로 합니다. 관리 클러스터와 Cluster API를
이미 운영한다면 CAPRKE2를 검토하십시오. 말목은 SSH 가능한 기존 머신을 대상으로
작업 PC에서 실행하며, 말목 전용 관리 컨트롤러나 노드 에이전트를 요구하지 않습니다.

### 업무용 프로덕션 환경에 바로 써도 되나요?

현재 알파 단계이며 프로덕션 준비 완료를 보증하지 않습니다.
[검증·미검증 구성](#검증-범위)을 확인하고, 도입 전에 실제 사용 환경에서 검증하십시오.

[자세한 FAQ와 도구 비교 →](docs/about/comparison.ko.md)

## 노드에서 무엇을 바꾸는가

말목이 root를 요구하는 이유는 호스트를 준비하기 때문입니다. 권한을 주기 전에
무엇을 하는지 알 수 있어야 하므로, 소스가 아니라 여기에 전부 적습니다.

- `br_netfilter`·`overlay`를 적재하고 `/etc/modules-load.d/90-malmok.conf`를
  써서 재부팅 후에도 유지되게 합니다.
- `/etc/sysctl.d/90-malmok.conf`를 씁니다. IPv4·IPv6 포워딩, 양쪽 계열의
  bridge netfilter, inotify 한도 상향.
- swap을 끄고 `/etc/fstab`의 해당 줄을 주석 처리합니다(원본은
  `/etc/fstab.malmok.bak`에 백업). 둘 다 필요합니다. kubelet은 swap이 켜져
  있으면 기동을 거부하고, fstab에 남은 항목은 다음 부팅에 swap을 되살립니다.
  swap을 유지해야 하는 곳은 `os.disableSwap: false`를 적으면 이 단계가 아예
  돌지 않습니다. 대신 `kubernetes.kubeletArgs`에 `fail-swap-on=false`를 함께
  적어야 하며, 이는 도구가 몰래 넣어 주는 대신 검증이 요구합니다.
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

### 마법사로 구축

기존 설정 파일 없이 시작할 수 있습니다.

```bash
# 말목 설치 — 이미 설치했다면 생략
curl -fsSL https://malmok.dev/install.sh | sh

# 마법사에서 설정·점검 후 구축
malmok apply --tui --lang ko
```

마법사가 `cluster.yaml`을 생성합니다. 대상 노드 주소와 접근 정보는 입력해야 합니다.
말목 설치 후 노드에 접속하지 않고 화면만 체험하려면
`malmok apply --demo --lang ko`를 실행하십시오.

### 다른 방법: 구성 파일로 실행

반복·자동화가 필요하다면 위 YAML 예제를 실제 환경에 맞게 `cluster.yaml`로
저장하고 아래 명령을 실행하십시오. 마법사에서 구축을 완료했다면 다시 설치할 필요는 없습니다.

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

아래 각 행은 검증 매트릭스가 명시된 릴리스에서, 명시된 노드 수와 네트워크
조건으로 실제 수행한 결과입니다. 11개 케이스 중 10개가 0.95.0 기준으로
통과합니다 — 에어갭이 마지막이었고, 거기 도달하기까지 결함 다섯 개를 더
드러냈습니다. 11번째인 서버 3대 장애 케이스는 그 뒤에 추가됐고 0.96.2에서
통과합니다.

| 구성 | 상태 | 근거 |
|---|---|---|
| 단일 노드 (control-plane + etcd) | **실측 검증** | 매트릭스 `idc-single`·`cilium-traefik-single`, 0.95.0. IDC 프로덕션 구축 완주 |
| 2노드 (server + agent) | **실측 검증** | 매트릭스 `canal-pair`·`homelab-full`·`byocert-lb`, 0.95.0 |
| 1노드에서 2노드로 확장(grow) | **실측 검증** | 매트릭스 `grow-to-two`, 0.95.0 |
| 중단 후 재개 | **실측 검증** | 매트릭스 `resume-after-kill`, 0.95.0 — `l1-bootstrap/service` 도중 강제 종료 후 재실행 완주 |
| 재적용이 아무것도 바꾸지 않음 | **실측 검증** | 매트릭스 `reapply-changes-nothing`, 0.95.0 — 전 단계 관찰 후 건너뜀 |
| RKE2 업그레이드 | **실측 검증** | 매트릭스 `upgrade-two`, 0.95.0 — v1.35.8+rke2r1로 구축 후 v1.36.4+rke2r1로 노드 하나씩 업그레이드, kubelet 보고 버전으로 확인 |
| Cilium 데이터플레인 + Gateway API | **실측 검증** | 매트릭스, 0.95.0. 외부 IP 도달 확인 |
| private-CA 리스너 인증서 | **실측 검증** | 매트릭스 `homelab-full`, 0.95.0 |
| 반입(BYO) 인증서 | **실측 검증** | 매트릭스 `byocert-lb`, 0.95.0 |
| ACME HTTP-01 인증서 | **실측 검증** | 프로덕션 클러스터에서 Let's Encrypt 발급, TLS 1.3·체인·호스트명 확인 |
| ACME DNS-01 인증서(와일드카드) | 미검증 | DNS 존 자격증명 필요 |
| 3서버 HA (etcd 쿼럼) | **실측 검증** | 매트릭스 `ha-failover`, 0.96.2: VIP 아래 서버 3대, VIP를 쥔 서버를 재부팅 — VIP 이전, etcd 3개 중 2개로 쓰기 성공, 재부팅한 서버가 스스로 복귀. 서버 3대 업그레이드는 미검증. `rke2-killall.sh`로 서버를 멈추면 VIP가 그 서버에 남음 — [복구 가이드](docs/guides/upgrade-and-recovery.ko.md) |
| 에어갭 설치 (외부 통신 차단) | **실측 검증** | 매트릭스 `airgap-pair`, 0.95.0: egress를 현장 방화벽처럼 DROP. 반입 아티팩트로 RKE2·Cilium, 파일로 반입한 플랫폼 차트(`registry.chartDir`), 반입 번들에서 전 이미지, local-path 볼륨 바인딩, 메트릭 DB 구동까지 — 밖에서 받아온 것 없음. 10개 케이스는 수정이 들어갈 때마다 세 번에 나눠 실행했고 한 번에 쓸어담은 것이 아님 |
| local-path 스토리지 | **실측 검증** | 매트릭스, 0.95.0 — 관측 스택을 설치하는 모든 케이스에서 볼륨 바인딩 |
| Longhorn / NFS 스토리지 | **미구현** | StorageClass 없는 클러스터를 만드는 대신 문서를 거부 |
| 현장 보유 CSI(`byo-csi`) | **미검증** | StorageClass 존재만 확인하는 단계이며, 실제 현장 CSI로 돌려본 적 없음 |
| 프록시 환경 | **미검증** | 스키마만 존재 |
| 외부 레지스트리 미러 | **미검증** | 0.93.0부터 노드에 실제 전달되지만, 레지스트리를 두고 하드웨어에서 돌려본 적 없음 |
| 관측(VictoriaMetrics) | **실측 검증** | 매트릭스, 0.95.0 — 이 도구가 만든 클러스터에서 메트릭 DB가 서빙. 프로덕션 클러스터 19개 대상 수집·저장 확인 |

검증 매트릭스는 8개 축의 조합을 정의합니다 — 네트워크 모드가 그중 하나라
에어갭은 별도 실행이 아니라 매트릭스의 한 행입니다 — 그리고 커버리지를
**테스트로 강제**합니다: 새 값을 추가하고 어느 조합에서도 쓰지 않으면
오프라인 테스트가 실패합니다. 설계와 실행 방법은
[`docs/40-verification-matrix.md`](docs/40-verification-matrix.md).

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

공식 한국어 문서는 [malmok.dev/ko/](https://malmok.dev/ko/)에서 제공합니다. 문서 첫 페이지가
프로젝트 소개와 설치·운영 가이드를 함께 제공하며, README는 도입 여부를 빠르게
판단하는 짧은 경로로 유지합니다.

- [설치](docs/getting-started/installation.md)
- [빠른 시작](docs/getting-started/quick-start.md)
- [에어갭 설치](docs/guides/air-gap.md)
- [설정 레퍼런스](docs/reference/configuration.md)
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
