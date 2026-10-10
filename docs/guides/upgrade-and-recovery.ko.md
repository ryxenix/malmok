# 업그레이드와 복구

말목은 관측한 클러스터 상태를 기준으로 업그레이드와 중단된 실행을 이어갑니다.

## RKE2 업그레이드

목표 버전을 검토하고 환경에 맞게 지정합니다.

=== "Bash"

    ```bash
    TARGET_RKE2=v1.36.3+rke2r1
    malmok upgrade --to "$TARGET_RKE2"
    ```

=== "Fish"

    ```fish
    set TARGET_RKE2 v1.36.3+rke2r1
    malmok upgrade --to "$TARGET_RKE2"
    ```

노드를 한 대씩 업그레이드하고 `Ready`로 돌아온 뒤 다음 노드로 넘어갑니다.

!!! info "검증 범위"

    서버 3대 구성은 구축, 서버 하나를 잃는 장애, 그리고 업그레이드까지
    검증했습니다. 매트릭스 `upgrade-three`가 한 마이너 버전으로 구축한 뒤
    한 대씩 다음 버전으로 올리고, 이후 모든 kubelet이 새 버전을 보고합니다.
    각 행이 무엇을 담는지는
    [검증 매트릭스](../40-verification-matrix.md)에서 확인하십시오.

### 반입 자재로 업그레이드하기

문서에 `kubernetes.artifactPath`가 있으면 업그레이드는 각 노드의 그 디렉터리에서
설치하고 아무것도 내려받지 않습니다. 그 디렉터리에는 구축할 때의 릴리스가 들어
있으므로, 업그레이드 전에 **모든 노드**에서 내용을 목표 릴리스의 자재로 바꿔
넣습니다. `install.sh`, `rke2.linux-<arch>.tar.gz`, `sha256sum-<arch>.txt`,
그리고 폐쇄망이면 이미지 아카이브(`rke2-images.linux-<arch>.tar.zst`, Cilium
데이터플레인이면 `rke2-images-cilium.linux-<arch>.tar.zst`)까지입니다.

어떤 노드도 drain하기 전에 `UP-006`이 각 노드의 디렉터리를 읽고, tarball 안의
`rke2` 바이너리를 실행해 버전을 확인합니다. 디렉터리가 없거나, 빠진 파일이
있거나, 읽을 수 없거나, 다른 릴리스가 들어 있으면 해당 노드를 지목해 업그레이드를
멈추며 아무것도 바꾸지 않습니다. RKE2 자재 이름에는 버전이 없어서, 구축용으로
놓아 둔 디렉터리와 업그레이드용으로 바꿔 넣은 디렉터리를 구별하는 방법은 이것뿐입니다.

!!! info "검증 범위"

    매트릭스 케이스가 아닌 수동 실행으로 실제 장비에서 검증했습니다. 서버 1대와
    에이전트 1대를 반입 자재로 v1.35.8+rke2r1에 구축했고, 디렉터리에 v1.35.8이
    남아 있는 동안에는 `UP-006`이 아무것도 바꾸지 않고 거부했으며, 디렉터리를
    교체한 뒤 v1.36.4+rke2r1로 올렸습니다. 처음에는 노드 자신의 외부 통신만,
    두 번째는 파드 통신까지 막았고, 두 번째에는 서버 설치 단계와 에이전트 재시작
    단계에서 각각 실행을 끊은 뒤 아래 방법으로 마쳤습니다. 서버 3대, 그리고
    끊긴 것이 아니라 도중에 실패하는 에어갭 업그레이드는 아직 돌려보지 않았습니다.
    이 변경 이전 릴리스는 업그레이드할 때 반입 자재 경로를 아예 읽지 않았습니다.

### 중단된 업그레이드 마치기

같은 `malmok upgrade --to` 를 다시 실행합니다. `--resume RUN_ID` 를 붙여도 되고
붙이지 않아도 됩니다. 서버가 먼저 올라가므로, 서버 뒤에서 멈춘 업그레이드는
컨트롤 플레인이 목표 버전이고 에이전트가 뒤처져 있거나 cordon된 노드가 남은
상태가 됩니다. 이 상태에서는 사전 검사가 통과하고 실행이 남은 노드를 마칩니다.
클러스터가 이미 목표 버전으로 보고하는 노드는 다시 drain하지 않고 uncordon은
그대로 실행하므로, 둘 사이에서 멈춘 노드도 다시 작업을 받습니다. 모든 노드가
목표 버전이고 작업을 받는 상태일 때만 `UP-002`가 거부합니다. 이 수정 전에는
그 상태에서 다시 실행하면 "새 버전이 아님"으로 거부되어 에이전트가 그대로
남았습니다.

## 중단 후 재개

실행마다 상태와 JSONL 이벤트를 기록합니다. 말목이 출력한 실행 ID를 지정합니다.

```bash
# RUN_ID를 실제 실행 ID로 교체
malmok apply --resume RUN_ID
```

입력 문서의 해시가 달라졌다면 재개를 거부합니다. 중단된 작업이 다른 목표 설정으로
완료되는 것을 방지하기 위한 동작입니다.

## 같은 문서 재적용

```bash
malmok apply -f cluster.yaml
```

각 단계는 변경 전에 목표 상태에 도달했는지 확인합니다. 같은 문서를 재적용하면
이미 충족한 작업은 건너뛰며 필요한 변경만 수행합니다.

## VIP 가 있는 클러스터에서 서버를 멈출 때

`rke2-killall.sh` 는 RKE2 의 컨테이너를 강제로 죽여서 멈추고, kube-vip 도 그중
하나입니다. 그렇게 죽은 kube-vip 은 VIP 를 내려놓을 틈이 없어서, RKE2 가
멈춘 뒤에도 그 서버의 네트워크 카드에 VIP 주소가 남습니다. 다른 서버가 VIP 를
넘겨받아도, 멈춘 서버의 kube-vip 이 다시 뜨기 전까지는 — RKE2 를 다시 켜거나
기계를 재부팅하기 전까지는 — 두 서버가 같은 주소에 응답합니다. 멈춘 서버에서
보낸 요청은 자기 자신에게 가서 거부되고, 같은 네트워크의 다른 기계도 그쪽으로
갈 수 있습니다.

추정이 아니라 측정한 것입니다. 서버 3대 장애 케이스에서, 멈춘 서버는 다른
서버가 VIP 를 가져간 뒤로도 30초 동안 주소를 쥐고 있었고, 자기 kube-vip 이 다시
뜨고 나서야 내려놓았습니다.

멈춘 채로 둘 서버라면 주소를 떼어 두십시오. 먼저 어느 인터페이스에 붙어
있는지 찾고, 지웁니다.

```bash
ip -4 -o addr show | grep -F <VIP>
sudo ip addr del <VIP>/32 dev <interface>
```

재부팅하면 저절로 떨어집니다. 서버가 통째로 죽는 장애에서는 이 문제가 남지
않는 이유입니다.

## 스냅숏으로 etcd 복구

말목에는 복구 명령이 없습니다. 복구는 RKE2 자체의 cluster reset이며, 아래
절차는 랩에서 리허설한 그대로입니다.

### 서버 밖에 보관할 것

- 스냅숏 파일: `kubernetes.etcd.snapshotTarget` 또는
  `/var/lib/rancher/rke2/server/db/snapshots`
- 서버 토큰: `/var/lib/rancher/rke2/server/token`
- `/etc/rancher/rke2/`: `config.yaml`, `registries.yaml`
- `/var/lib/rancher/rke2/server/manifests/`: 말목이 기록한 매니페스트
- RKE2 버전, 폐쇄망이라면 해당 릴리스 자재

!!! danger "스냅숏과 토큰이 함께 있으면 클러스터 비밀값을 풀 수 있습니다"

    스냅숏에는 클러스터 CA 키와 비밀값이 들어 있습니다. 스냅숏과 서버 토큰을
    함께 가진 사람은 이를 읽을 수 있으므로 둘 다 보호하고, 가능하면 따로
    보관하십시오.

작업 전에 스냅숏을 직접 뜨려면:

```bash
sudo rke2 etcd-snapshot save --name before-maintenance
```

### 같은 서버에서 복구

`SNAPSHOT`을 파일 이름으로 바꿉니다.

```bash
sudo systemctl stop rke2-server
sudo rke2 server --cluster-reset --cluster-reset-restore-path=/var/lib/rancher/rke2/server/db/snapshots/SNAPSHOT
sudo systemctl start rke2-server
```

reset은 끝나면 스스로 종료합니다. 마지막 줄 "Managed etcd cluster membership
has been reset, restart without --cluster-reset flag now"는 error 수준으로
찍히지만 정상 종료입니다.

### 서버를 잃었을 때

같은 주소의 대체 서버에서:

1. 같은 RKE2 버전을 설치합니다. 폐쇄망이면 `INSTALL_RKE2_ARTIFACT_PATH`로 반입
   자재에서 설치합니다.
2. `/etc/rancher/rke2/`와 `/var/lib/rancher/rke2/server/manifests/`를 되돌립니다.
3. 스냅숏 파일을 `/var/lib/rancher/rke2/server/db/snapshots/` 아래에 둡니다.
4. 보관한 토큰으로 reset한 뒤 서비스를 시작합니다. `TOKEN_FILE`은 토큰을 둔
   위치입니다.

=== "Bash"

    ```bash
    sudo rke2 server --cluster-reset --cluster-reset-restore-path=/var/lib/rancher/rke2/server/db/snapshots/SNAPSHOT --token="$(sudo cat TOKEN_FILE)"
    sudo systemctl enable --now rke2-server
    ```

=== "Fish"

    ```fish
    sudo rke2 server --cluster-reset --cluster-reset-restore-path=/var/lib/rancher/rke2/server/db/snapshots/SNAPSHOT --token=(sudo cat TOKEN_FILE)
    sudo systemctl enable --now rke2-server
    ```

`config.yaml`에 토큰이 있다면 보관한 토큰과 같아야 하며, 다르면 RKE2가 시작되지
않습니다. 에이전트는 등록 주소를 바라보고 에이전트 토큰을 갖고 있으므로 스스로
다시 붙습니다.

### 서버가 여러 대일 때

리허설하지 않았습니다. RKE2 절차는 모든 서버에서 `rke2-server`를 멈추고, 한
대에서 reset 후 시작한 다음, 나머지 서버마다 `/var/lib/rancher/rke2/server/db/`를
지우고 시작하는 것입니다.

!!! info "검증 범위"

    매트릭스 케이스가 아닌 수동 리허설입니다. 랩에서 서버 1대와 에이전트 1대,
    v1.36.4, 외부 통신 허용 상태로 했습니다. 같은 서버에서 복구한 경우와, 같은
    서버를 초기화한 뒤 보관한 토큰·설정·매니페스트로 재설치해 복구한 경우 모두,
    스냅숏 전에 만든 ConfigMap은 돌아오고 스냅숏 후에 만든 것은 사라졌으며 두
    노드가 Ready, 에이전트도 다시 붙었습니다. 이어서 노드와 파드 통신을 막은
    채, 막 에어갭 업그레이드를 마친 클러스터에서 두 경우를 다시 해 같은 결과를
    얻었습니다. 서버 3대는 아직 리허설하지 않았습니다. 내장 레지스트리 미러를 쓰는 클러스터에서는
    `rke2 etcd-snapshot save`가 "Unknown flag --embedded-registry found in
    config.yaml, skipping"을 출력하지만, 스냅숏 생성과 복구는 정상이었습니다.

## 근거부터 확인

인증서 유지보수를 준비한다면 v0.96.4 이상의 [읽기 전용 만료 점검](certificates.md)도
활용할 수 있습니다. RKE2 재시작이나 인증서 갱신은 하지 않으며, 별도의 실행 기록을
만들어 해당 ID로 보고서를 생성할 수 있습니다.

```bash
malmok report
```

오류를 진단할 때는 진단 코드와 해당 실행의 `events.jsonl` 마지막 부분을 확인합니다.
폐기한 코드 번호는 재사용하지 않으므로 이전 리포트와 티켓에서도 의미가 유지됩니다.

[진단 코드 찾기 →](../99-codes.md)
