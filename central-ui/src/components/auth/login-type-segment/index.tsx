import React, { type FC } from 'react';
import { LoginType } from '@/types';
import { Segment, type SegmentProps, SegmentVariant } from '@odigos/ui-kit/components';

interface LoginTypeSegmentProps {
  selected: LoginType;
  setSelected: (loginType: LoginType) => void;
}

const OPTIONS: SegmentProps<LoginType>['options'] = [
  { value: LoginType.OIDC, label: 'OIDC' },
  { value: LoginType.SAML, label: 'SAML' },
];

const LoginTypeSegment: FC<LoginTypeSegmentProps> = ({ selected, setSelected }) => {
  return <Segment fullWidth variant={SegmentVariant.Filled} options={OPTIONS} selected={selected} setSelected={setSelected} />;
};

export { LoginTypeSegment, type LoginTypeSegmentProps };
