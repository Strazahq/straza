import type * as React from "react";
import { Button } from "@/components/ui/button";
import { Chip, Pill, StatusChip, isStatus } from "@/components/chip";
import { Facts } from "@/components/draft-parts";
import { EntraLink } from "@/components/entra-link";
import { HelpTip } from "@/components/help-tip";
import { KIND_ICON } from "@/components/kind-icon";
import { MUTED, Waiting } from "@/components/user-parts";
import { UserKey } from "@/components/user-key";
import { type DeviceRow, type UserDetail, keyPosture, listDevices } from "@/lib/api";
import { userKind } from "@/lib/user-look";
import { dataOf, useRead } from "@/lib/user-read";
import {
  DEVICES_FACT, DEVICES_LINE, KEY_FACT, NO_DEVICE, PHONES_FACT, READING_DEVICES, READING_KEY, REVOKE, SUBJECT_DEVICES, SUBJECT_KEY, UNKNOWN_PLATFORM, enrolledOn, phones, revokeName,
} from "@/lib/user-words";
import { dayWord } from "@/lib/words";

// deviceName is the device's own name, or the head of its id when the
// enrolment carried none.
const deviceName = (d: DeviceRow) => d.name || d.id.slice(0, 8);

type Props = {
  user: UserDetail;
  nonce: number;
  onRevokeDevice: (device: DeviceRow, label: string) => void;
  onRevokeKey: () => void;
  onChanged: () => void;
};

// UserDevices is the Devices and keys tab: an AI agent's assertion key, the
// machines that hold an enroll credential, the count of approver phones,
// and an agent's Entra agent identity. It reads when the tab opens.
export function UserDevices({ user, nonce, onRevokeDevice, onRevokeKey, onChanged }: Props) {
  const agent = userKind(user) === "agent";
  const devices = useRead(true, () => listDevices(user.id), SUBJECT_DEVICES, nonce);
  const key = useRead(agent, () => keyPosture(user.id), SUBJECT_KEY, nonce);
  const list = dataOf(devices);
  const posture = dataOf(key);
  const phoneCount = user.counts?.approver_devices || 0;
  const rows: { label: string; value: React.ReactNode }[] = [];
  if (agent) {
    rows.push({
      label: KEY_FACT,
      value: posture ? <UserKey id={user.id} name={user.username} posture={posture} onRevoke={onRevokeKey} onChanged={onChanged} /> : <Waiting read={key} word={READING_KEY} subject={SUBJECT_KEY} />,
    });
  }
  rows.push({
    label: DEVICES_FACT,
    value: (
      <>
        <HelpTip label={DEVICES_FACT} text={DEVICES_LINE} />
        <div className="flex min-w-0 flex-1 flex-col gap-2" data-devices>
          <Waiting read={devices} word={READING_DEVICES} subject={SUBJECT_DEVICES} />
          {list && list.length === 0 && <span className="text-text-2">{NO_DEVICE}</span>}
          {(list || []).map((d) => (
            <div key={d.id} className="flex flex-wrap items-center gap-2" data-device={d.id}>
              <span className="font-semibold">{deviceName(d)}</span>
              <Chip tone="plain">{d.platform || UNKNOWN_PLATFORM}</Chip>
              <span className={MUTED}>{enrolledOn(dayWord(d.enrolled_at))}</span>
              {isStatus(d.status) ? <StatusChip status={d.status} /> : <Chip tone="danger">{d.status}</Chip>}
              <Button variant="outline" size="sm" aria-label={revokeName(deviceName(d))} onClick={() => onRevokeDevice(d, deviceName(d))}>{REVOKE}</Button>
            </div>
          ))}
        </div>
      </>
    ),
  });
  if (phoneCount > 0) rows.push({ label: PHONES_FACT, value: <Pill icon={KIND_ICON.phone}>{phones(phoneCount)}</Pill> });
  return (
    <div className="flex flex-col gap-6" data-user-devices>
      <Facts rows={rows} />
      {agent && <EntraLink user={user} />}
    </div>
  );
}
