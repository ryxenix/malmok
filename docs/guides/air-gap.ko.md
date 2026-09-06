# 에어갭 설치

폐쇄망 구축에는 `malmok-airgap` 바이너리 외에도 클러스터가 소비하는 모든 자재를
반입해야 합니다. 문서에는 각 자재가 놓인 위치를 명시합니다.

에어갭은 일반 검증 매트릭스의 축에 아직 포함되지 않았습니다. 별도 2노드 랩
실행 근거와 현재 제한은 [검증 매트릭스](../40-verification-matrix.md)와
[README 검증 범위](https://github.com/ryxenix/malmok/blob/main/README.ko.md#검증-범위)를 확인하십시오.

## 반입할 자재

인터넷 연결이 가능한 준비 머신에서 다음을 확보합니다.

- 운영자 아키텍처에 맞는 Linux `malmok-airgap` 바이너리
- 선택한 버전의 RKE2 tarball, 체크섬 파일과 RKE2의 `install.sh`
- 데이터플레인에 필요한 RKE2 이미지 아카이브
- Gateway API CRD를 활성화한다면 해당 번들
- 클러스터에서 실행할 애플리케이션 이미지
- cert-manager, 관측 또는 Argo CD를 활성화한다면 해당 Helm 차트 미러

말목의 에어갭 바이너리는 Helm과 k9s를 포함합니다. RKE2, Kubernetes 이미지나
애플리케이션 자재까지 포함하는 것은 아닙니다.

## 노드에 배치

각 노드의 같은 경로에 해당 노드 아키텍처용 RKE2 자재를 배치합니다.
다음은 디렉터리 형태의 예시입니다.

```text
/srv/malmok/rke2/
├── install.sh
├── rke2.linux-amd64.tar.gz
├── sha256sum-amd64.txt
├── rke2-images-*.linux-amd64.tar.zst
└── gateway-api-*-standard-install.yaml
```

arm64 노드에는 arm64 파일을 사용합니다. 필요한 정확한 이미지 목록은 RKE2
버전과 데이터플레인에 따라 다릅니다. preflight가 실제 파일을 검사하도록 하십시오.

## 오프라인 공급원 지정

아래는 완전한 문서가 아닌 설정 일부입니다. 현장에 맞는 topology, PKI,
storage와 platform 설정을 추가해야 합니다.

```yaml
metadata:
  profile: airgap-ubuntu
network:
  mode: airgap
os:
  ntpServers:
    - 10.20.0.10
kubernetes:
  version: v1.36.3+rke2r1
  artifactPath: /srv/malmok/rke2
registry:
  mode: internal
  systemDefaultRegistry: harbor.internal.example
  chartRepo: oci://harbor.internal.example/charts
```

RKE2 이미지 아카이브를 노드에 미리 적재하는 구성이라면 내부 레지스트리로
이미지 이름을 바꾸는 대신 embedded 미러를 사용할 수 있습니다.

!!! danger "이미지와 차트를 각각 준비하십시오"

    `systemDefaultRegistry`는 컨테이너 이미지 공급원이고 `chartRepo`는 Helm
    차트 공급원입니다. 하나만 미러링하면 다른 자재를 받으려고 외부에 접속할 수 있습니다.

## 반입 자재 검증

```bash
malmok plan -f cluster.yaml --validate-only
malmok preflight -f cluster.yaml
malmok plan -f cluster.yaml
```

아티팩트 경로, 이미지·차트 공급원, NTP와 인증서 체인 관련 결과를 확인합니다.
문서 검증 통과만으로 폐쇄망 종단 간 검증이 끝난 것은 아닙니다.

## 구축과 기록 보관

```bash
malmok apply -f cluster.yaml
malmok report
```

입력 문서, 릴리스 체크섬, 이벤트 스트림과 인계 리포트를 함께 보관하십시오.
추후 재현과 감사에 필요한 근거입니다.
