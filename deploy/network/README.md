# VDI 내부 네트워크

2026-10-08 원격 OpenStack의 admin 프로젝트에 수동 생성했다. Kolla 인프라 설정은 변경하지 않았다.

- 네트워크: vdi-private, Geneve, private, MTU 1442
- UUID: 18efdf70-aa4a-407f-abe6-690e541ed710
- 서브넷: vdi-private-subnet, 10.20.0.0/24
- 게이트웨이: 10.20.0.1
- DHCP: 활성화, 10.20.0.100~10.20.0.200
- DNS: 1.1.1.1, 8.8.8.8
- 라우터: vdi-router, ext-net 외부 게이트웨이, SNAT 활성화
- 현재 라우터 외부 IP: 172.30.0.109 (자동 할당이므로 재생성 시 달라질 수 있음)

백엔드의 단일 실행 설정 `.env`에 반영하고 컨테이너를 재생성했다. 이제 기본 `docker compose up -d`도 이 원격 설정을 사용한다:

```dotenv
OS_NETWORK_ID=18efdf70-aa4a-407f-abe6-690e541ed710
OS_SECURITY_GROUPS=vdi-rdp-check
```

백엔드 생성 코드는 이미 OS_NETWORK_ID를 Nova networks UUID로 사용하므로 코드 변경은 필요하지 않다. 이 UUID는 현재 설치에만 유효하며 재구성 시 Playbook 출력값을 사용한다.

## Playbook에 통합

Kolla 배포 완료 후 인증 가능한 Actions Runner에서 실행한다. 암호는 Playbook에 저장하지 않으며 기존 OpenStack RC 또는 clouds.yaml을 사용한다. 생성할 프로젝트의 인증을 사용해야 한다. 현재 구성의 인증 프로젝트는 admin이다.

Runner에도 파일을 `/home/actions-runner/kolla/vdi-network/`에 복사했다.

```bash
source /home/actions-runner/kolla-venv/bin/activate
source /etc/kolla/admin-openrc.sh
cd /home/actions-runner/kolla/vdi-network
ansible-galaxy collection install -r requirements.yml -p ./collections
ANSIBLE_COLLECTIONS_PATH="$PWD/collections" ansible-playbook -i localhost, playbook.yml --check
ANSIBLE_COLLECTIONS_PATH="$PWD/collections" ansible-playbook -i localhost, playbook.yml
```

`vars.yml`에 이름·CIDR·DHCP 범위·DNS·외부망을 정의한다. 기존 리소스를 이름으로 찾아 재사용한다. tenant network driver는 Kolla/Neutron 기본값을 사용하며 현재 환경에서는 Geneve이다. 마지막 task가 OS_NETWORK_ID를 출력하므로 앱의 실행 설정/Secret에 전달한다. Playbook은 앱 설정 파일이나 Kolla globals.yml을 직접 변경하지 않는다. openstack.cloud 2.6.0과 openstacksdk>=1.0.0이 필요하다.

모듈 기준: [network](https://docs.ansible.com/projects/ansible/latest/collections/openstack/cloud/network_module.html), [subnet](https://docs.ansible.com/projects/ansible/latest/collections/openstack/cloud/subnet_module.html), [router](https://docs.ansible.com/projects/ansible/latest/collections/openstack/cloud/router_module.html).

## 접근 경로

내부망을 생성하면 Compute의 physnet1 직접 연결 없이 VM을 생성할 수 있다. 라우터 SNAT는 외부로 나가는 통신을 제공하지만 Mac의 backend/guacd에서 10.20.0.0/24로 들어오는 경로를 자동으로 만들지는 않는다. backend와 guacd 모두 VM 고정 IP:3389에 도달해야 한다. 현재 백엔드는 Floating IP를 자동 생성하지 않고 고정 IP를 우선 사용한다. RDP 연결 완료에는 별도 라우팅/접근 구성이 필요하다.

## 이번 검증 결과

- 테스트 VM은 Nova ACTIVE, vdi-private의 10.20.0.108 할당. 기존 PortBindingFailed 재현 안 됨.
- 앱은 RUNNING/PENDING까지 전환. Mac에서 VM의 3389 접근은 timeout이므로 RDP READY/Guacamole 화면 성공을 의미하지 않는다.
- 검증 VM은 DELETE API로 정리했다.
- Runner에서 Playbook 문법 검사와 실제 재실행 성공: ok=5, changed=0, failed=0. 수동 생성 리소스를 그대로 재사용했다.
- openstack.cloud 2.6.0의 subnet check mode에서 라이브러리 오류를 확인하여, Playbook은 check mode에서 생성/변경 task를 명시적으로 건너뛴다. --check는 기존 네트워크 UUID 조회만 수행하며 변경 내용을 예측하지 않는다. 최초 생성은 실제 실행이 필요하다.
