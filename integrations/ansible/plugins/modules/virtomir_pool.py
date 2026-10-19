#!/usr/bin/python
# -*- coding: utf-8 -*-
# Copyright: (c) 2026, Onur Baydas
# GNU General Public License v3.0+ (see LICENSE or https://www.gnu.org/licenses/gpl-3.0.txt)

from __future__ import absolute_import, division, print_function

__metaclass__ = type

DOCUMENTATION = r'''
---
module: virtomir_pool
short_description: Manage storage pools in VirtoMir
description:
  - Create, start, stop, or delete storage pools (dir, logical, zfs, netfs, iscsi) in VirtoMir.
  - Supports check mode and idempotent operation.
version_added: "0.7.0"
author:
  - Onur Baydaş (@onurbaydas)
options:
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
  host:
    description: Hostname or IP of NFS/iSCSI server.
    type: str
  export:
    description: NFS export path on the server.
    type: str
  target:
    description: Target IQN for iSCSI.
    type: str
  devices:
    description: List of block devices (e.g. for creating new LVM volume groups).
    type: list
    elements: str
  autostart:
    description: Autostart pool on host boot.
    type: bool
    default: true
  state:
    description: Desired pool state.
    type: str
    choices: [present, absent, active, inactive]
    default: present
  url:
    description: VirtoMir address (or VIRTOMIR_URL).
    type: str
  token:
    description: API token (or VIRTOMIR_TOKEN).
    type: str
  validate_certs:
    description: Check the host's certificate.
    type: bool
    default: true
  host_member:
    description: Cluster member to manage through url.
    type: str
'''

EXAMPLES = r'''
- name: Create local directory pool
  onurbaydas.virtomir.virtomir_pool:
    name: ssd-pool
    type: dir
    path: /mnt/fast-nvme/images
    autostart: true
    state: present

- name: Create NFS shared storage pool
  onurbaydas.virtomir.pool:
    name: nfs-shared
    type: netfs
    path: /mnt/nfs-shared
    host: 192.168.1.50
    export: /srv/nfs/virtomir
    state: active

- name: Remove unused storage pool
  onurbaydas.virtomir.pool:
    name: old-pool
    state: absent
'''

RETURN = r'''
pool:
  description: The storage pool details as returned by the VirtoMir API.
  returned: when state != absent
  type: dict
diff:
  description: Differences between previous and desired state.
  returned: always
  type: dict
'''

from ansible.module_utils.basic import AnsibleModule

try:
    from ansible_collections.onurbaydas.virtomir.plugins.module_utils.virtomir import (
        APIError, Client, common_args
    )
except ImportError:
    from ..module_utils.virtomir import APIError, Client, common_args


def main():
    args = common_args()
    args.update(
        name=dict(type='str', required=True),
        type=dict(type='str', choices=['dir', 'logical', 'zfs', 'netfs', 'iscsi'], default='dir'),
        path=dict(type='str'),
        source=dict(type='str'),
        host=dict(type='str'),
        export=dict(type='str'),
        target=dict(type='str'),
        devices=dict(type='list', elements='str'),
        autostart=dict(type='bool', default=True),
        state=dict(type='str', choices=['present', 'absent', 'active', 'inactive'], default='present'),
    )
    module = AnsibleModule(argument_spec=args, supports_check_mode=True)
    p = module.params
    c = Client(module)

    name = p['name']
    pool_type = p['type']
    state = p['state']
    autostart = p['autostart']
    path = p.get('path')
    source = p.get('source')
    host = p.get('host')
    export = p.get('export')
    target = p.get('target')
    devices = p.get('devices')

    changed = False
    diff = {'before': {}, 'after': {}}

    try:
        current = c.pool(name)

        if state == 'absent':
            if current is not None:
                changed = True
                diff['before'] = {'state': 'present', 'name': name}
                diff['after'] = {'state': 'absent'}
                if not module.check_mode:
                    c.delete_pool(name)
            module.exit_json(changed=changed, diff=diff)

        if current is None:
            changed = True
            desired_spec = {
                'name': name,
                'type': pool_type,
                'autostart': autostart,
                'state': state,
            }
            if path:
                desired_spec['path'] = path
            if source:
                desired_spec['source'] = source
            if host:
                desired_spec['host'] = host
            if export:
                desired_spec['export'] = export
            if target:
                desired_spec['target'] = target
            if devices:
                desired_spec['devices'] = devices

            diff['before'] = {}
            diff['after'] = desired_spec

            if module.check_mode:
                module.exit_json(changed=True, diff=diff, pool=None)

            api_spec = {'name': name, 'type': pool_type}
            if path:
                api_spec['path'] = path
            if source:
                api_spec['source'] = source
            if host:
                api_spec['host'] = host
            if export:
                api_spec['export'] = export
            if target:
                api_spec['target'] = target
            if devices:
                api_spec['devices'] = devices

            c.create_pool(api_spec)

            if autostart is False:
                c.set_pool_autostart(name, False)

            if state == 'inactive':
                c.stop_pool(name)
            elif state == 'active':
                cur_pool = c.pool(name)
                if cur_pool and not cur_pool.get('active'):
                    c.start_pool(name)

            module.exit_json(changed=True, diff=diff, pool=c.pool(name))

        # Current pool exists
        cur_autostart = current.get('autostart', False)
        if autostart is not None and cur_autostart != autostart:
            changed = True
            diff.setdefault('before', {})['autostart'] = cur_autostart
            diff.setdefault('after', {})['autostart'] = autostart
            if not module.check_mode:
                c.set_pool_autostart(name, autostart)

        is_active = current.get('active', False)
        if state == 'active' and not is_active:
            changed = True
            diff.setdefault('before', {})['active'] = False
            diff.setdefault('after', {})['active'] = True
            if not module.check_mode:
                c.start_pool(name)
        elif state == 'inactive' and is_active:
            changed = True
            diff.setdefault('before', {})['active'] = True
            diff.setdefault('after', {})['active'] = False
            if not module.check_mode:
                c.stop_pool(name)

        pool_res = c.pool(name) if not module.check_mode and changed else current
        module.exit_json(changed=changed, diff=diff, pool=pool_res)

    except APIError as e:
        module.fail_json(msg=str(e), status=e.status)


if __name__ == '__main__':
    main()
