#!/usr/bin/python
# -*- coding: utf-8 -*-
# Copyright: (c) 2026, Onur Baydas
# GNU General Public License v3.0+ (see LICENSE or https://www.gnu.org/licenses/gpl-3.0.txt)

from __future__ import absolute_import, division, print_function

__metaclass__ = type

DOCUMENTATION = r'''
---
module: virtomir_snapshot
short_description: Manage VM snapshots in VirtoMir
description:
  - Create, revert, or delete virtual machine snapshots in VirtoMir.
  - Idempotent: creating an existing snapshot is a no-op; reverting when already current/active is a no-op.
  - Supports check mode.
version_added: "0.8.0"
author:
  - Onur Baydaş (@onurbaydas)
options:
  machine:
    description: Target virtual machine name or UUID.
    type: str
    required: true
  name:
    description: Snapshot name.
    type: str
    required: true
  description:
    description: Optional human-readable description.
    type: str
    default: ''
  memory:
    description: Save running memory (RAM) state alongside disks.
    type: bool
    default: false
  state:
    description: Desired snapshot state.
    type: str
    choices: [present, absent, reverted]
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
  host:
    description: Cluster member to manage through url.
    type: str
'''

EXAMPLES = r'''
- name: Take snapshot before maintenance
  onurbaydas.virtomir.virtomir_snapshot:
    machine: web-1
    name: pre-upgrade-20261019
    description: "Before system upgrade"
    state: present

- name: Revert to previous snapshot
  onurbaydas.virtomir.snapshot:
    machine: web-1
    name: pre-upgrade-20261019
    state: reverted

- name: Remove snapshot
  onurbaydas.virtomir.snapshot:
    machine: web-1
    name: pre-upgrade-20261019
    state: absent
'''

RETURN = r'''
snapshot:
  description: The snapshot details as returned by the VirtoMir API.
  returned: when state in [present, reverted]
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
        machine=dict(type='str', required=True),
        name=dict(type='str', required=True),
        description=dict(type='str', default=''),
        memory=dict(type='bool', default=False),
        state=dict(type='str', choices=['present', 'absent', 'reverted'], default='present'),
    )
    module = AnsibleModule(argument_spec=args, supports_check_mode=True)
    p = module.params
    c = Client(module)

    machine_ref = p['machine'].strip()
    snap_name = p['name'].strip()
    desc = (p.get('description') or '').strip()
    memory = p['memory']
    state = p['state']

    changed = False
    diff = {'before': {}, 'after': {}}

    try:
        vm = c.domain(machine_ref)
        if vm is None:
            module.fail_json(msg="Machine %s not found" % machine_ref)
        uuid = vm['uuid']

        current = c.snapshot(uuid, snap_name)

        if state == 'absent':
            if current is not None:
                changed = True
                diff['before'] = current
                diff['after'] = {'state': 'absent'}
                if not module.check_mode:
                    c.delete_snapshot(uuid, snap_name)
            module.exit_json(changed=changed, diff=diff)

        if state == 'present':
            if current is None:
                changed = True
                spec = {'name': snap_name, 'description': desc}
                diff['before'] = {}
                diff['after'] = spec
                if not module.check_mode:
                    c.create_snapshot(uuid, spec)
                    current = c.snapshot(uuid, snap_name)
                module.exit_json(changed=True, diff=diff, snapshot=current)
            else:
                # Idempotent: existing snapshot is a no-op
                module.exit_json(changed=False, diff=diff, snapshot=current)

        if state == 'reverted':
            if current is None:
                module.fail_json(msg="Snapshot %s not found on machine %s to revert" % (snap_name, machine_ref))

            is_current = current.get('current', False)
            if not is_current:
                changed = True
                diff['before'] = {'current': False}
                diff['after'] = {'current': True}
                if not module.check_mode:
                    c.revert_snapshot(uuid, snap_name)
                    current = c.snapshot(uuid, snap_name)
                module.exit_json(changed=True, diff=diff, snapshot=current)
            else:
                # Idempotent: reverting when active is no-op
                module.exit_json(changed=False, diff=diff, snapshot=current)

    except APIError as e:
        module.fail_json(msg=str(e), status=e.status)


if __name__ == '__main__':
    main()
