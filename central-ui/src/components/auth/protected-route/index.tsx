import { type FC, type ReactNode, useEffect, useMemo } from 'react';
import { ROUTES } from '@/utils';
import { useUserStore } from '@/store';
import { GeneralResult } from '@/types';
import { VERIFY_TOKEN } from '@/graphql';
import { useMutation } from '@apollo/client';
import { usePathname, useRouter } from 'next/navigation';
import { CenterThis, FadeLoader } from '@odigos/ui-kit/components';

interface ProtectedRouteProps {
  children: ReactNode;
}

const ProtectedRoute: FC<ProtectedRouteProps> = ({ children }) => {
  const router = useRouter();
  const pathname = usePathname();
  const { accessToken, clearAuth } = useUserStore();

  const isOnSignInPath = useMemo(() => pathname === ROUTES.SIGN_IN, [pathname]);

  const goToSignIn = () => {
    clearAuth();
    router.push(ROUTES.SIGN_IN);
  };
  const goToConnections = () => {
    router.push(ROUTES.CONNECTIONS);
  };

  const [verifyToken, { loading }] = useMutation<{ verifyToken: GeneralResult }, { token: string }>(VERIFY_TOKEN, {
    onError: (error) => {
      console.error(error);
      goToSignIn();
    },
    onCompleted: (data) => {
      if (!data.verifyToken.success) {
        goToSignIn();
      } else if (data.verifyToken.success && isOnSignInPath) {
        goToConnections();
      }
    },
  });

  useEffect(() => {
    if (accessToken) {
      verifyToken({ variables: { token: accessToken } });
    } else if (!accessToken && !isOnSignInPath) {
      goToSignIn();
    }
  }, [accessToken, isOnSignInPath]);

  if (!isOnSignInPath && (loading || !accessToken)) {
    return (
      <CenterThis style={{ height: '100vh' }}>
        <FadeLoader scale={2} />
      </CenterThis>
    );
  }

  return children;
};

export default ProtectedRoute;
