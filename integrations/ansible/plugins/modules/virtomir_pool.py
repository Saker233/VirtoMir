#!/usr/bin/python
# -*- coding: utf-8 -*-

# Copyright: (c) 2026, Onur Baydaş <onurbaydas1@gmail.com>
# GNU General Public License v3.0+ (see COPYING or https://www.gnu.org/licenses/gpl-3.0.txt)

from __future__ import absolute_import, division, print_function
__metaclass__ = type

DOCUMENTATION = r'''
---
module: virtomir_pool
short_description: Manage storage pools in VirtoMir
description:
  - Create, start, stop, or delete storage pools (dir, logical, zfs, netfs, iscsi) in VirtoMir.
version_added: "0.7.0"
author:
  - Onur Baydaş (@onurbaydas)
options:
  url:
    description: Base URL of the VirtoMir daemon.
    type: str
    required: true
  token:
    description: API token with Admin role.
    type: str
    required: true
    no_log: true
  validate_certs:
    description: Validate TLS certificates.
    type: bool
    default: true
  name:
    description: Storage pool name.
    type: str
    required: true
  type:
    description: Storage pool type.
    type: str
    choices: [dir, logical, zfs, netfs, iscsi]
    default: dir
  path:
    description: Host directory path (for dir/netfs).
    type: str
  source:
    description: Volume group, zpool or server host.
    type: str
  autostart:
    description: Autostart pool on host boot.
    type: bool
    default: true
  state:
    description: Desired pool state.
    type: str
    choices: [present, absent, active, inactive]
    default: present
'''

EXAMPLES = r'''
- name: Create local directory pool
  onurbaydas.virtomir.virtomir_pool:
    url: https://virtomir.local:8420
    token: vmt_...
    validate_certs: false
    name: ssd-pool
    type: dir
    path: /mnt/fast-nvme/images
    autostart: true
    state: present
'''

from ansible.module_utils.basic import AnsibleModule
from ansible.module_utils.urls import fetch_url
import json


def call_api(module, method, endpoint, data=None):
    base = module.params['url'].rstrip('/')
    url = "{0}/api/v1{1}".format(base, endpoint)
    headers = {
        'Authorization': 'Bearer ' + module.params['token'],
        'Content-Type': 'application/json',
    }
    payload = json.dumps(data) if data is not None else None
    resp, info = fetch_url(module, url, method=method, data=payload, headers=headers)
    status = info.get('status', -1)
    body = None
    if resp:
        try:
            body = json.loads(resp.read().decode('utf-8'))
        except Exception:
            body = None
    if status >= 400:
        msg = (body.get('error') if isinstance(body, dict) else None) or info.get('msg', 'API error')
        module.fail_json(msg="VirtoMir API {0} {1} failed ({2}): {3}".format(method, endpoint, status, msg))
    return body


def main():
    module = AnsibleModule(
        argument_spec=dict(
            url=dict(type='str', required=True),
            token=dict(type='str', required=True, no_log=True),
            validate_certs=dict(type='bool', default=True),
            name=dict(type='str', required=True),
            type=dict(type='str', choices=['dir', 'logical', 'zfs', 'netfs', 'iscsi'], default='dir'),
            path=dict(type='str'),
            source=dict(type='str'),
            autostart=dict(type='bool', default=True),
            state=dict(type='str', choices=['present', 'absent', 'active', 'inactive'], default='present'),
        ),
        supports_check_mode=True,
    )

    name = module.params['name']
    state = module.params['state']

    pools = call_api(module, 'GET', '/storage') or []
    current = next((p for p in pools if p.get('name') == name), None)

    changed = False

    if state == 'absent':
        if current is not None:
            if not module.check_mode:
                call_api(module, 'DELETE', "/storage/{0}".format(name))
            changed = True
        module.exit_json(changed=changed)

    if current is None:
        if not module.check_mode:
            payload = {
                'name': name,
                'type': module.params['type'],
                'path': module.params.get('path'),
                'source': module.params.get('source'),
                'autostart': module.params['autostart'],
            }
            call_api(module, 'POST', '/storage', payload)
        changed = True
    else:
        is_active = current.get('active', False)
        if state == 'active' and not is_active:
            if not module.check_mode:
                call_api(module, 'POST', "/storage/{0}/start".format(name))
            changed = True
        elif state == 'inactive' and is_active:
            if not module.check_mode:
                call_api(module, 'POST', "/storage/{0}/stop".format(name))
            changed = True

    module.exit_json(changed=changed, pool=current)


if __name__ == '__main__':
    main()
