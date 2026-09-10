---
hide:
  - toc
---

<div class="malmok-hero" markdown>

![말목](img/malmok-wordmark.png){ .malmok-hero__wordmark }

# 반복 가능한 구축, 중단 후 재개, 기록으로 남는 RKE2

<p class="malmok-hero__lead">
말목은 <code>cluster.yaml</code>과 SSH 접근 권한으로 직접 관리하는 인프라에
RKE2 클러스터를 구축합니다. 변경 전에 실측하고, 중단된 실행을 이어가며,
운영자가 인계할 수 있는 근거를 남깁니다.
</p>

<div class="malmok-hero__actions">
  <a href="getting-started/quick-start/" class="md-button md-button--primary">빠른 시작</a>
  <a href="guides/air-gap/" class="md-button">에어갭 설치</a>
  <a href="https://github.com/ryxenix/malmok" class="md-button">GitHub</a>
</div>

<p class="malmok-kicker">베어메탈 · VM · 온프레미스 · DMZ · 에어갭</p>

</div>

!!! warning "알파 단계"

    홈랩과 평가 환경에서 사용해 보고 피드백을 주십시오. 업무 환경 도입
    전에는 검증 범위와 제한을 확인하십시오.

    단일 노드와 2노드 구성은 실제 장비에서 업그레이드·재개·재적용까지 반복
    검증했습니다. 3서버 HA, 프록시와 외부 레지스트리 미러는 미검증입니다.
    에어갭은 검증됐습니다 — egress를 끊은 상태에서 RKE2·차트·전 이미지를
    반입 파일로 설치합니다. 자세한 범위는 [에어갭 가이드](guides/air-gap.md)에
    있습니다. 문서 스키마는 `v1alpha1`이며 마이너 릴리스 사이에 바뀔 수
    있습니다.

    버전은 호환성 약속이지 완성도 점수가 아닙니다. **1.0 은 스키마가 `v1` 이
    되고 *미검증* 으로 표시된 행들이 검증될 때입니다.**

## 나에게 필요한 도구인가요?

이미 준비된 Linux 서버와 SSH 접근 권한이 있다면 시작할 수 있습니다.
개인 클러스터든 팀의 클러스터든, 구성 파일로 RKE2를 직접 구축하고 관리하려는 사용자를 위한 도구입니다.

### 개인 프로젝트·홈랩에서

집의 서버·미니 PC나 개인 VM에 RKE2를 직접 구축하고 운영하려는 사용자에게:

- 수동 설치 절차를 매번 찾아보지 않고 구성 파일로 클러스터를 다시 구축하고 싶을 때
- 노드를 실험하거나 홈랩을 재설치하면서 반복 작업을 줄이고 싶을 때
- 별도 관리 클러스터 없이 작업 PC의 SSH와 CLI로 시작하고 싶을 때

[첫 클러스터 구축하기 →](getting-started/quick-start.md)

### 업무·팀 환경에서

고객사가 준비한 서버에 솔루션을 반복 납품하는 개발사·SI 팀·구축 엔지니어와
사내 인프라 담당자에게. **RKE2를 사용하기로 결정했지만 클러스터 기반은 아직
구축해야 하는 상황**을 대상으로 합니다.

- AI·문서처리·검색·데이터 플랫폼 등을 납품하며 고객사별 RKE2 설치를 공통 절차로 정리하고 싶을 때
- 변경 전에 실제 서버 상태와 구축을 막는 요인을 확인하고 싶을 때
- 중단된 구축을 재개하고 검토·인수인계를 위한 실행 기록을 남겨야 할 때
- 폐쇄망 설치가 필요해 에어갭 지원 조건을 확인하고 준비하려 할 때

[에어갭 지원 조건 →](guides/air-gap.md) · [업그레이드와 복구 →](guides/upgrade-and-recovery.md)

같은 조건의 공공기관·공기업 납품/구축 사업도 사용 상황에 포함됩니다.
이는 구축 사례의 유형이지, 조달 적격성이나 보안 인증을 주장하는 것이 아닙니다.
[고객사 구축 시 적합성과 책임 범위 →](about/comparison.md#customer-delivery)

**현재 알파 단계입니다. 개인·업무 환경 모두 도입 전
[검증 범위](40-verification-matrix.md)를 확인하십시오. 위 사용 사례가 프로덕션 준비 완료를 뜻하지는 않습니다.**
VM 생성이나 범용 구성 관리, 애플리케이션 배포 도구를 대체하지 않습니다.

<div class="grid cards" markdown>

-   :material-radar: **먼저 실측합니다**

    ---

    실제 리스너를 열고 다른 노드에서 접속해 봅니다. 변경 전에 호스트와 통신 경로를 확인합니다.

-   :material-backup-restore: **중단돼도 이어갑니다**

    ---

    실행 상태를 기록해 중단된 단계부터 재개합니다. 이미 완료한 작업은 상태를 확인하고 건너뜁니다.

-   :material-file-document-check: **결과를 인계합니다**

    ---

    JSONL 이벤트, 안정적인 진단 코드와 인계 리포트로 무엇을 실행했고 왜 실행했는지 남깁니다.

</div>

## 설치

Bash와 Fish에서 같은 명령을 사용합니다. 설치 스크립트는 POSIX `sh`로 실행되며
Linux·macOS, amd64·arm64를 판별하고 공개된 `SHA256SUMS`로 바이너리를 검증합니다.

```bash
curl -fsSL https://malmok.dev/install.sh | sh
```

[설치 옵션 보기 →](getting-started/installation.md)

## 마법사로 시작하기

말목 설치 후 설정 파일을 미리 만들지 않고 마법사를 실행할 수 있습니다.

```bash
malmok apply --tui --lang ko
```

대상 노드 주소와 SSH 접근 정보를 입력하면 마법사가 `cluster.yaml`을 생성하고,
점검과 구축을 안내합니다.

## 구성 파일로 반복·자동화하기 { #cluster-document }

마법사의 선행 작업이 아니라, 파일로 구성을 관리하려는 사용자를 위한 별도 경로입니다.

아래 예제를 `cluster.yaml`로 저장하고 주소, SSH 계정, 키 경로와 RKE2 버전을
실제 환경에 맞게 바꾸십시오.

```yaml title="cluster.yaml"
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

`homelab` 프로파일이 생략한 기본값을 채웁니다. `malmok plan`에서 확정된 값과
출처를 확인하십시오. 서버를 추가하기 전에는 안정적인 등록 주소를 마련해야 합니다.

```bash
# 노드에 접속하지 않고 문서 검증
malmok plan -f cluster.yaml --validate-only

# 노드를 변경하지 않고 실측
malmok preflight -f cluster.yaml

# 확정된 계획 검토
malmok plan -f cluster.yaml

# 구축하고 인계 리포트 확인
malmok apply -f cluster.yaml
malmok report
```

## 전체 흐름 보기

한국어 TUI의 데모 모드 녹화입니다. 데모는 노드에 접속하지 않습니다.
실제 구축에서도 같은 엔진을 사용합니다.

<div class="malmok-demo" markdown>

![말목 한국어 TUI: 설정, 사전 점검, 설치, 결과](img/tui-wizard-ko.gif)

</div>

## 어디까지 담당하는가

말목은 호스트 준비와 RKE2, 데이터플레인, Gateway API 및 선택한 플랫폼 구성 요소의
설치를 담당합니다. 머신 프로비저닝, 방화벽 정책과 애플리케이션 배포는 사용자가 소유합니다.

범용 구성 관리에는 Ansible이 적합합니다. 관리 클러스터와 Cluster API를 이미
운영한다면 CAPRKE2도 검토하십시오. 말목은 SSH 가능한 머신에서 시작해 구축과
검증, 인계를 한 흐름으로 수행하려는 운영자를 위한 도구입니다.

[자주 묻는 질문과 도구 비교 →](about/comparison.md)

## 말목 — 이름의 뜻

**말목은 경계를 표시하거나 지반을 보강하기 위해 땅에 단단히 박는 나무 말뚝을
뜻하는 한국어입니다.** Malmok은 이를 로마자로 옮긴 이름입니다. 첫 서버를
기준점으로 세우고 나머지 노드를 연결해 클러스터의 기반을 다진다는 의미를 담았습니다.
