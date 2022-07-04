import type { NextPage } from "next";
import { getConfiguration } from "@/utils/config";

const Home: NextPage = () => {
  return <div className="text-3xl">This is the Overview Screen</div>;
};

export const getServerSideProps = async () => {
  const config = await getConfiguration();
  if (!config) {
    return {
      redirect: {
        destination: "/setup",
        permanent: false,
      },
    };
  }

  return {
    props: {},
  };
};

export default Home;
